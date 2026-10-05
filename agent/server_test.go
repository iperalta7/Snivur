package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"snivur/v0/shared"
)

const testKey = "s3cret"

func newTestAgent(fr *fakeRunner) http.Handler {
	return newAgentServer(Config{Addr: ":0", APIKey: testKey}, DockerRuntime{Run: fr.Run})
}

func doLaunch(t *testing.T, h http.Handler, key *string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/launch", strings.NewReader(body))
	if key != nil {
		r.Header.Set("X-API-Key", *key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// decodeSingle asserts the body is exactly one JSON value and decodes it.
func decodeSingle(t *testing.T, body []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("body %q contains more than one JSON value", body)
	}
}

func ptr(s string) *string { return &s }

func launchBody(serverID, name, image string) string {
	b, _ := json.Marshal(shared.LaunchRequest{
		ServerID: serverID, Name: name, Game: "g",
		Config: map[string]string{"image": image},
	})
	return string(b)
}

func TestLaunchHandler(t *testing.T) {
	valid := launchBody("sid1", "mc1", "alpine")
	tests := []struct {
		name       string
		ac         string
		key        *string
		body       string
		runnerOut  string
		runnerErr  error
		wantStatus int
		wantCalls  int
		wantErrSub string // substring expected in ErrorResponse.Error
	}{
		{name: "success", ac: "AC1", key: ptr(testKey), body: valid, runnerOut: "cid42\n", wantStatus: 200, wantCalls: 1},
		{name: "runner error", ac: "AC2", key: ptr(testKey), body: valid, runnerOut: "docker: pull access denied\n", runnerErr: errors.New("exit status 125"), wantStatus: 500, wantCalls: 1, wantErrSub: "docker: pull access denied"},
		{name: "name --privileged", ac: "AC3", key: ptr(testKey), body: launchBody("sid1", "--privileged", "alpine"), wantStatus: 400, wantCalls: 0, wantErrSub: "name"},
		{name: "image -v/:/host", ac: "AC3", key: ptr(testKey), body: launchBody("sid1", "mc1", "-v/:/host"), wantStatus: 400, wantCalls: 0, wantErrSub: "image"},
		{name: "image with space", ac: "AC3", key: ptr(testKey), body: launchBody("sid1", "mc1", "alpine --privileged"), wantStatus: 400, wantCalls: 0},
		{name: "missing image", ac: "AC3", key: ptr(testKey), body: `{"server_id":"s","name":"a","config":{}}`, wantStatus: 400, wantCalls: 0},
		{name: "empty server_id", key: ptr(testKey), body: launchBody("", "mc1", "alpine"), wantStatus: 400, wantCalls: 0, wantErrSub: "server_id"},
		{name: "malformed JSON", key: ptr(testKey), body: `{"server_id":`, wantStatus: 400, wantCalls: 0},
		{name: "empty body", key: ptr(testKey), body: ``, wantStatus: 400, wantCalls: 0},
		{name: "missing key", ac: "AC4", key: nil, body: valid, wantStatus: 401, wantCalls: 0},
		{name: "empty key header", ac: "AC4", key: ptr(""), body: valid, wantStatus: 401, wantCalls: 0},
		{name: "wrong key", ac: "AC4", key: ptr("nope"), body: valid, wantStatus: 401, wantCalls: 0},
		{name: "key prefix", ac: "AC4", key: ptr(testKey[:3]), body: valid, wantStatus: 401, wantCalls: 0},
		{name: "key with suffix", ac: "AC4", key: ptr(testKey + "x"), body: valid, wantStatus: 401, wantCalls: 0},
		{name: "wrong key + bad body still 401", ac: "AC4", key: ptr("nope"), body: `garbage`, wantStatus: 401, wantCalls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.ac+" "+tt.name, func(t *testing.T) {
			fr := &fakeRunner{out: []byte(tt.runnerOut), err: tt.runnerErr}
			w := doLaunch(t, newTestAgent(fr), tt.key, tt.body)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := len(fr.Calls()); got != tt.wantCalls {
				t.Fatalf("runner called %d times, want %d", got, tt.wantCalls)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			if tt.wantStatus == 200 {
				var resp shared.LaunchResponse
				decodeSingle(t, w.Body.Bytes(), &resp)
				if resp.ServerID != "sid1" || resp.ContainerID != "cid42" {
					t.Errorf("response = %+v, want {sid1 cid42}", resp)
				}
				return
			}
			var er shared.ErrorResponse
			decodeSingle(t, w.Body.Bytes(), &er)
			if er.Error == "" {
				t.Error("ErrorResponse.error is empty")
			}
			if tt.wantErrSub != "" && !strings.Contains(er.Error, tt.wantErrSub) {
				t.Errorf("error %q missing %q", er.Error, tt.wantErrSub)
			}
		})
	}
}

func TestLaunchRouting(t *testing.T) {
	fr := &fakeRunner{out: []byte("id")}
	h := newTestAgent(fr)
	r := httptest.NewRequest(http.MethodGet, "/launch", nil)
	r.Header.Set("X-API-Key", testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /launch = %d, want 405", w.Code)
	}
	if len(fr.Calls()) != 0 {
		t.Error("runner called on GET")
	}
}

// AC5
func TestAgentLoadConfig(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantErr  bool
		wantAddr string
		wantKey  string
	}{
		{"AC5 key unset", map[string]string{}, true, "", ""},
		{"AC5 key empty, addr set", map[string]string{"SNIVUR_AGENT_ADDR": ":9"}, true, "", ""},
		{"defaults", map[string]string{"SNIVUR_AGENT_API_KEY": "k"}, false, ":8000", "k"},
		{"addr override", map[string]string{"SNIVUR_AGENT_API_KEY": "k", "SNIVUR_AGENT_ADDR": "127.0.0.1:1234"}, false, "127.0.0.1:1234", "k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadConfig(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !strings.Contains(err.Error(), "SNIVUR_AGENT_API_KEY") {
					t.Errorf("error %q should name the missing variable", err)
				}
				return
			}
			if cfg.Addr != tt.wantAddr || cfg.APIKey != tt.wantKey {
				t.Errorf("cfg = %+v, want Addr=%q APIKey=%q", cfg, tt.wantAddr, tt.wantKey)
			}
		})
	}
}
