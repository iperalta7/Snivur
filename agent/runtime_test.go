package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"snivur/v0/shared"
)

type runnerCall struct {
	name string
	args []string
}

// fakeRunner records every invocation and returns canned output.
type fakeRunner struct {
	mu    sync.Mutex
	calls []runnerCall
	out   []byte
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, runnerCall{name: name, args: append([]string(nil), args...)})
	return f.out, f.err
}

func (f *fakeRunner) Calls() []runnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runnerCall(nil), f.calls...)
}

// AC1
func TestDockerRuntimeStart(t *testing.T) {
	req := shared.LaunchRequest{
		ServerID: "abc123",
		Name:     "mc1",
		Game:     "minecraft",
		Config:   map[string]string{"image": "itzg/minecraft-server:latest", "other": "ignored"},
	}
	wantArgs := []string{"run", "-d", "--rm", "--name", "mc1", "--label", "snivur.server_id=abc123", "itzg/minecraft-server:latest"}

	tests := []struct {
		name    string
		out     string
		err     error
		wantID  string
		wantErr []string // substrings expected in error
	}{
		{"plain id", "deadbeef", nil, "deadbeef", nil},
		{"trailing newline trimmed", "deadbeef\n", nil, "deadbeef", nil},
		{"surrounding whitespace trimmed", "  \r\ndeadbeef \r\n", nil, "deadbeef", nil},
		{"runner error wraps output", "  Unable to find image\n", errors.New("exit status 125"), "", []string{"exit status 125", "Unable to find image"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeRunner{out: []byte(tt.out), err: tt.err}
			id, err := DockerRuntime{Run: fr.Run}.Start(context.Background(), req)

			calls := fr.Calls()
			if len(calls) != 1 {
				t.Fatalf("runner called %d times, want 1", len(calls))
			}
			if calls[0].name != "docker" {
				t.Errorf("command = %q, want docker", calls[0].name)
			}
			if !reflect.DeepEqual(calls[0].args, wantArgs) {
				t.Errorf("args = %q\nwant  %q", calls[0].args, wantArgs)
			}

			if tt.wantErr != nil {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, tt.err) {
					t.Errorf("error %v does not wrap runner error", err)
				}
				for _, s := range tt.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q missing %q", err, s)
					}
				}
				if strings.Contains(err.Error(), "\n") {
					t.Errorf("error %q should contain trimmed output", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestDockerRuntimePassesContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	var got context.Context
	rt := DockerRuntime{Run: func(c context.Context, _ string, _ ...string) ([]byte, error) {
		got = c
		return []byte("id"), nil
	}}
	if _, err := rt.Start(ctx, shared.LaunchRequest{ServerID: "s", Name: "n", Config: map[string]string{"image": "i"}}); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Value(key{}) != "v" {
		t.Error("Start did not pass its context to the runner")
	}
}
