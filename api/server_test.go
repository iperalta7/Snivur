package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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

// AC6
func TestCreateServerPassThrough(t *testing.T) {
	tests := []struct {
		name        string
		agentStatus int
		agentBody   string
	}{
		{"AC6 success 200 with container_id", 200, `{"server_id":"x","container_id":"cid42"}`},
		{"agent 500 passes through", 500, `{"error":"docker run: exit status 125: boom"}`},
		{"agent 401 passes through", 401, `{"error":"invalid API key"}`},
		{"agent 400 passes through", 400, `{"error":"bad"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := newFakeAgent(t, tt.agentStatus, tt.agentBody)
			h := withControllerKey(newControllerServer(Config{AgentURL: fa.URL, AgentAPIKey: testKey, ControllerAPIKey: testControllerKey}, fa.Client()))
			w := post(t, h, validBody)

			if w.Code != tt.agentStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.agentStatus)
			}
			if w.Body.String() != tt.agentBody {
				t.Errorf("body = %q, want passthrough %q", w.Body.String(), tt.agentBody)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}

			hits := fa.Hits()
			if len(hits) != 1 {
				t.Fatalf("agent hit %d times, want 1", len(hits))
			}
			hit := hits[0]
			if hit.method != http.MethodPost || hit.path != "/launch" {
				t.Errorf("agent got %s %s, want POST /launch", hit.method, hit.path)
			}
			if hit.key != testKey {
				t.Errorf("X-API-Key = %q, want controller's key %q", hit.key, testKey)
			}
			if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(hit.req.ServerID) {
				t.Errorf("forwarded server_id = %q, want NewID() format", hit.req.ServerID)
			}
			if hit.req.Name != "mc1" || hit.req.Game != "minecraft" || hit.req.Config["image"] != "alpine" {
				t.Errorf("forwarded request = %+v", hit.req)
			}
			if tt.agentStatus == 200 {
				var lr shared.LaunchResponse
				singleJSON(t, w.Body.Bytes(), &lr)
				if lr.ContainerID != "cid42" {
					t.Errorf("container_id = %q", lr.ContainerID)
				}
			}
		})
	}
}

func TestCreateServerUniqueIDs(t *testing.T) {
	fa := newFakeAgent(t, 200, `{}`)
	h := withControllerKey(newControllerServer(Config{AgentURL: fa.URL, AgentAPIKey: testKey, ControllerAPIKey: testControllerKey}, fa.Client()))
	post(t, h, validBody)
	post(t, h, validBody)
	hits := fa.Hits()
	if len(hits) != 2 || hits[0].req.ServerID == hits[1].req.ServerID {
		t.Fatalf("expected two distinct server IDs, got %+v", hits)
	}
}

func TestCreateServerTrailingSlashAgentURL(t *testing.T) {
	fa := newFakeAgent(t, 200, `{}`)
	h := withControllerKey(newControllerServer(Config{AgentURL: fa.URL + "/", AgentAPIKey: testKey, ControllerAPIKey: testControllerKey}, fa.Client()))
	post(t, h, validBody)
	if hits := fa.Hits(); len(hits) != 1 || hits[0].path != "/launch" {
		t.Fatalf("hits = %+v, want one POST /launch", hits)
	}
}

// AC6
func TestCreateServerAgentUnreachable(t *testing.T) {
	// Start and immediately close a server to get a URL nothing listens on.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()

	h := withControllerKey(newControllerServer(Config{AgentURL: url, AgentAPIKey: testKey, ControllerAPIKey: testControllerKey}, &http.Client{Timeout: 5 * time.Second}))
	w := post(t, h, validBody)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %q)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var er shared.ErrorResponse
	singleJSON(t, w.Body.Bytes(), &er)
	if er.Error == "" {
		t.Error("empty error message")
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
			h := withControllerKey(newControllerServer(Config{AgentURL: fa.URL, AgentAPIKey: testKey, ControllerAPIKey: testControllerKey}, fa.Client()))
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
