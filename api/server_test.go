package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"snivur/v0/shared"
)

const testKey = "ctrl-key"

type agentHit struct {
	method, path, key string
	req               shared.LaunchRequest
}

// fakeAgent is an httptest server that records requests and replies with a
// fixed status/body.
type fakeAgent struct {
	*httptest.Server
	mu     sync.Mutex
	hits   []agentHit
	status int
	body   string
}

func newFakeAgent(t *testing.T, status int, body string) *fakeAgent {
	fa := &fakeAgent{status: status, body: body}
	fa.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req shared.LaunchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		fa.mu.Lock()
		fa.hits = append(fa.hits, agentHit{r.Method, r.URL.Path, r.Header.Get("X-API-Key"), req})
		fa.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fa.status)
		io.WriteString(w, fa.body)
	}))
	t.Cleanup(fa.Close)
	return fa
}

func (fa *fakeAgent) Hits() []agentHit {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return append([]agentHit(nil), fa.hits...)
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/servers", strings.NewReader(body)))
	return w
}

func singleJSON(t *testing.T, body []byte, v any) {
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

const validBody = `{"name":"mc1","game":"minecraft","config":{"image":"alpine"}}`

func TestCreateServerTrailingSlashAgentURL(t *testing.T) {
	fa := newFakeAgent(t, 200, `{}`)
	c := newHTTPAgentClient(fa.URL+"/", testKey, fa.Client())
	if _, err := c.Launch(context.Background(), shared.LaunchRequest{ServerID: "s", Name: "mc1", Config: map[string]string{"image": "alpine"}}); err != nil {
		t.Fatal(err)
	}
	if hits := fa.Hits(); len(hits) != 1 || hits[0].path != "/launch" {
		t.Fatalf("hits = %+v, want one POST /launch", hits)
	}
}

func TestCreateServerValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"AC3 name --privileged", `{"name":"--privileged","config":{"image":"alpine"}}`},
		{"AC3 image -v/:/host", `{"name":"mc1","config":{"image":"-v/:/host"}}`},
		{"missing image", `{"name":"mc1","config":{}}`},
		{"missing config", `{"name":"mc1"}`},
		{"malformed JSON", `{"name":`},
		{"empty body", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := newFakeAgent(t, 200, `{}`)
			h := withControllerKey(newControllerServer(NewStore(time.Now), NewRegistry(nil, HealthThresholds{}), newHTTPAgentClient(fa.URL, testKey, fa.Client()), testKey, testControllerKey))
			w := post(t, h, tt.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			var er shared.ErrorResponse
			singleJSON(t, w.Body.Bytes(), &er)
			if er.Error == "" {
				t.Error("empty error")
			}
			if n := len(fa.Hits()); n != 0 {
				t.Errorf("agent contacted %d times for invalid request", n)
			}
		})
	}
}

func TestControllerLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		want    Config
	}{
		{"key required", map[string]string{}, true, Config{}},
		{"controller key required", map[string]string{"SNIVUR_AGENT_API_KEY": "k"}, true, Config{}},
		{"defaults", map[string]string{"SNIVUR_AGENT_API_KEY": "k", "SNIVUR_CONTROLLER_API_KEY": "c"}, false, Config{Addr: ":8080", AgentURL: "http://localhost:8081", AgentAPIKey: "k", ControllerAPIKey: "c"}},
		{"overrides", map[string]string{"SNIVUR_AGENT_API_KEY": "k", "SNIVUR_CONTROLLER_API_KEY": "c", "SNIVUR_CONTROLLER_ADDR": ":1", "SNIVUR_AGENT_URL": "http://a:2"}, false, Config{Addr: ":1", AgentURL: "http://a:2", AgentAPIKey: "k", ControllerAPIKey: "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadConfig(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && cfg != tt.want {
				t.Errorf("cfg = %+v, want %+v", cfg, tt.want)
			}
		})
	}
}

func TestAgentClientTimeout(t *testing.T) {
	if got := newAgentClient().Timeout; got != 5*time.Minute {
		t.Errorf("agent client timeout = %v, want 5m", got)
	}
}
