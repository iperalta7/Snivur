package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"snivur/v0/shared"
)

// specListFormat is the --format argument exactly as written in ADR 0003.
// The `\t` is the two characters backslash-t, which the docker CLI expands
// to a tab; it must reach docker as a single argv element.
const specListFormat = `{{.ID}}\t{{.Label "snivur.server_id"}}\t{{.State}}`

// AC1: List shells out with exactly the argv from the spec.
func TestDockerRuntimeListArgv(t *testing.T) {
	fr := &fakeRunner{out: []byte("c1\tsrv1\trunning\n")}
	if _, err := (DockerRuntime{Run: fr.Run}).List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(calls))
	}
	want := []string{"ps", "-a", "--filter", "label=snivur.server_id", "--format", specListFormat}
	if calls[0].name != "docker" {
		t.Errorf("name = %q, want docker", calls[0].name)
	}
	if !reflect.DeepEqual(calls[0].args, want) {
		t.Errorf("args = %q\nwant   %q", calls[0].args, want)
	}
	if listFormat != specListFormat {
		t.Errorf("listFormat = %q, want %q", listFormat, specListFormat)
	}
}

// AC1: parsing real docker output with tabs, blank lines, malformed lines
// and CRLF line endings.
func TestDockerRuntimeListParses(t *testing.T) {
	out := "" +
		"aaa111\tsrv-1\trunning\r\n" +
		"\n" +
		"bbb222\tsrv-2\texited\n" +
		"this line is malformed\n" +
		"ccc333\tsrv-3\n" + // too few fields
		"ddd444\tsrv-4\trunning\textra\n" + // too many fields
		"eee555\t\trunning\n" + // empty server id
		"   \r\n" +
		"fff666\tsrv-6\tcreated" // no trailing newline
	fr := &fakeRunner{out: []byte(out)}
	got, err := DockerRuntime{Run: fr.Run}.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []shared.ContainerInfo{
		{ServerID: "srv-1", ContainerID: "aaa111", State: "running"},
		{ServerID: "srv-2", ContainerID: "bbb222", State: "exited"},
		{ServerID: "srv-6", ContainerID: "fff666", State: "created"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List =\n %+v\nwant\n %+v", got, want)
	}
}

// AC1: empty output is an empty, non-nil list (so it encodes as []).
func TestDockerRuntimeListEmpty(t *testing.T) {
	for _, out := range []string{"", "\n", "\r\n\r\n"} {
		fr := &fakeRunner{out: []byte(out)}
		got, err := DockerRuntime{Run: fr.Run}.List(context.Background())
		if err != nil {
			t.Fatalf("List(%q): %v", out, err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("List(%q) = %#v, want empty non-nil slice", out, got)
		}
	}
	if got := parseContainerList(nil); got == nil || len(got) != 0 {
		t.Errorf("parseContainerList(nil) = %#v, want empty non-nil slice", got)
	}
}

// AC1: a runner failure is an error that carries docker's output.
func TestDockerRuntimeListError(t *testing.T) {
	fr := &fakeRunner{out: []byte("Cannot connect to the Docker daemon\n"), err: errors.New("exit status 1")}
	got, err := DockerRuntime{Run: fr.Run}.List(context.Background())
	if err == nil {
		t.Fatalf("List = %v, want error", got)
	}
	for _, s := range []string{"exit status 1", "Cannot connect to the Docker daemon"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q missing %q", err, s)
		}
	}
}
