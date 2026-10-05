package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"snivur/v0/shared"
)

type runnerReply struct {
	out []byte
	err error
}

// seqRunner returns replies in order (one per call) and records every call.
type seqRunner struct {
	mu      sync.Mutex
	calls   []runnerCall
	replies []runnerReply
}

func (f *seqRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, runnerCall{name: name, args: append([]string(nil), args...)})
	if len(f.replies) == 0 {
		return nil, errors.New("seqRunner: unexpected call")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r.out, r.err
}

func (f *seqRunner) Calls() []runnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runnerCall(nil), f.calls...)
}

var (
	psCall   = runnerCall{"docker", []string{"ps", "-q", "--filter", "label=snivur.server_id=sid1"}}
	stopCall = runnerCall{"docker", []string{"stop", "c0ffee"}}
)

// AC7: Stop issues the exact docker ps and docker stop arguments.
func TestDockerRuntimeStop(t *testing.T) {
	tests := []struct {
		name      string
		replies   []runnerReply
		wantCalls []runnerCall
		wantErr   error  // checked with errors.Is
		errSubstr string // or a substring
	}{
		{
			name:      "found and stopped",
			replies:   []runnerReply{{out: []byte("c0ffee\n")}, {out: []byte("c0ffee\n")}},
			wantCalls: []runnerCall{psCall, stopCall},
		},
		{
			name:      "no container",
			replies:   []runnerReply{{out: []byte("")}},
			wantCalls: []runnerCall{psCall},
			wantErr:   ErrNotFound,
		},
		{
			name:      "whitespace only output is not found",
			replies:   []runnerReply{{out: []byte("  \n")}},
			wantCalls: []runnerCall{psCall},
			wantErr:   ErrNotFound,
		},
		{
			name:      "docker ps fails",
			replies:   []runnerReply{{out: []byte("Cannot connect to the Docker daemon"), err: errors.New("exit status 1")}},
			wantCalls: []runnerCall{psCall},
			errSubstr: "Cannot connect",
		},
		{
			name:      "docker stop fails",
			replies:   []runnerReply{{out: []byte("c0ffee\n")}, {out: []byte("Error response from daemon"), err: errors.New("exit status 1")}},
			wantCalls: []runnerCall{psCall, stopCall},
			errSubstr: "Error response from daemon",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &seqRunner{replies: tt.replies}
			err := DockerRuntime{Run: fr.Run}.Stop(context.Background(), "sid1")
			if got := fr.Calls(); !reflect.DeepEqual(got, tt.wantCalls) {
				t.Errorf("calls = %+v\nwant    %+v", got, tt.wantCalls)
			}
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.errSubstr != "":
				if err == nil || !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("err = %v, want containing %q", err, tt.errSubstr)
				}
				if errors.Is(err, ErrNotFound) {
					t.Errorf("runner failure reported as ErrNotFound: %v", err)
				}
			default:
				if err != nil {
					t.Errorf("err = %v", err)
				}
			}
		})
	}
}

func TestDockerRuntimeStopPassesContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	var seen []any
	run := func(c context.Context, name string, args ...string) ([]byte, error) {
		seen = append(seen, c.Value(key{}))
		return []byte("c1"), nil
	}
	if err := (DockerRuntime{Run: run}).Stop(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "v" || seen[1] != "v" {
		t.Errorf("contexts seen = %v", seen)
	}
}

func doStop(h http.Handler, key *string, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/servers/"+id+"/stop", nil)
	if key != nil {
		r.Header.Set("X-API-Key", *key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// AC7 / agent spec: POST /servers/{id}/stop returns 204, 404, 500, and is
// behind the API key.
func TestStopHandler(t *testing.T) {
	tests := []struct {
		name      string
		key       *string
		replies   []runnerReply
		status    int
		wantCalls int
	}{
		{"success", ptr(testKey), []runnerReply{{out: []byte("c0ffee")}, {}}, http.StatusNoContent, 2},
		{"not found", ptr(testKey), []runnerReply{{}}, http.StatusNotFound, 1},
		{"ps error", ptr(testKey), []runnerReply{{err: errors.New("boom")}}, http.StatusInternalServerError, 1},
		{"stop error", ptr(testKey), []runnerReply{{out: []byte("c0ffee")}, {err: errors.New("boom")}}, http.StatusInternalServerError, 2},
		{"missing key", nil, nil, http.StatusUnauthorized, 0},
		{"wrong key", ptr("nope"), nil, http.StatusUnauthorized, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &seqRunner{replies: tt.replies}
			h := newAgentServer(Config{APIKey: testKey}, DockerRuntime{Run: fr.Run})
			w := doStop(h, tt.key, "sid1")
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.status, w.Body)
			}
			calls := fr.Calls()
			if len(calls) != tt.wantCalls {
				t.Fatalf("runner calls = %+v, want %d", calls, tt.wantCalls)
			}
			if tt.wantCalls > 0 && !reflect.DeepEqual(calls[0], psCall) {
				t.Errorf("first call = %+v", calls[0])
			}
			if tt.status == http.StatusNoContent {
				if w.Body.Len() != 0 {
					t.Errorf("204 with body %q", w.Body)
				}
				return
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			var er shared.ErrorResponse
			decodeSingle(t, w.Body.Bytes(), &er)
			if er.Error == "" {
				t.Error("empty error")
			}
		})
	}
}

func TestStopHandlerRouting(t *testing.T) {
	fr := &seqRunner{}
	h := newAgentServer(Config{APIKey: testKey}, DockerRuntime{Run: fr.Run})
	r := httptest.NewRequest(http.MethodGet, "/servers/sid1/stop", nil)
	r.Header.Set("X-API-Key", testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET stop: status %d, want 405", w.Code)
	}
	if n := len(fr.Calls()); n != 0 {
		t.Errorf("runner called %d times", n)
	}
}

// AC8 (agent): body larger than 1 MiB returns 413 with ErrorResponse when the
// API key is valid; docker is never invoked.
func TestAgentBodyTooLarge(t *testing.T) {
	if maxBodyBytes != 1<<20 {
		t.Errorf("maxBodyBytes = %d, want 1 MiB", maxBodyBytes)
	}
	big := strings.Repeat("a", 1<<20)
	valid := launchBody("sid1", "mc1", "alpine")
	cases := map[string]string{
		"oversized string":        `{"server_id":"sid1","name":"mc1","config":{"image":"alpine","x":"` + big + `"}}`,
		"valid JSON then padding": valid + strings.Repeat(" ", 1<<20),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			fr := &fakeRunner{out: []byte("cid")}
			h := newTestAgent(fr)
			w := doLaunch(t, h, ptr(testKey), body)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413 (body %.200s)", w.Code, w.Body)
			}
			var er shared.ErrorResponse
			decodeSingle(t, w.Body.Bytes(), &er)
			if er.Error == "" {
				t.Error("empty error")
			}
			if n := len(fr.Calls()); n != 0 {
				t.Errorf("docker invoked %d times", n)
			}
		})
	}
	t.Run("under limit ok", func(t *testing.T) {
		fr := &fakeRunner{out: []byte("cid")}
		pad := (1 << 20) - len(valid) - 1
		if w := doLaunch(t, newTestAgent(fr), ptr(testKey), valid+strings.Repeat(" ", pad)); w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (%s)", w.Code, w.Body)
		}
	})
	t.Run("no key is 401 not 413", func(t *testing.T) {
		fr := &fakeRunner{}
		if w := doLaunch(t, newTestAgent(fr), nil, cases["oversized string"]); w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})
}

func TestAgentHTTPServerHardening(t *testing.T) {
	s := newHTTPServer(":0", http.NotFoundHandler())
	if s.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", s.ReadHeaderTimeout)
	}
}
