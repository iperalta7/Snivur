package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"snivur/v0/shared"
)

func newAgentsTestServer(clk *fakeClock) (http.Handler, *Registry) {
	reg := NewRegistry(clk.Now, HealthThresholds{})
	return withControllerKey(newControllerServer(NewStore(clk.Now), reg, &instantAgent{}, testKey, testControllerKey)), reg
}

func postHeartbeat(h http.Handler, key *string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/agents/heartbeat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != nil {
		r.Header.Set("X-API-Key", *key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func strp(s string) *string { return &s }

func hbJSON(t *testing.T, hb shared.Heartbeat) string {
	t.Helper()
	b, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// AC6: authentication on POST /agents/heartbeat.
func TestHeartbeatAuth(t *testing.T) {
	h, reg := newAgentsTestServer(newFakeClock())
	body := hbJSON(t, hbFor("a"))
	for name, key := range map[string]*string{
		"missing key": nil,
		"empty key":   strp(""),
		"wrong key":   strp("nope"),
		"key prefix":  strp(testKey[:3]),
	} {
		t.Run(name, func(t *testing.T) {
			assertError(t, postHeartbeat(h, key, body), http.StatusUnauthorized)
		})
	}
	if l := reg.List(); len(l) != 0 {
		t.Errorf("unauthenticated heartbeat registered %+v", l)
	}
}

// AC6: validation on POST /agents/heartbeat.
func TestHeartbeatValidation(t *testing.T) {
	h, reg := newAgentsTestServer(newFakeClock())
	noID := hbFor("a")
	noID.AgentID = ""
	noAddr := hbFor("a")
	noAddr.Address = ""
	for name, body := range map[string]string{
		"missing agent_id": hbJSON(t, noID),
		"missing address":  hbJSON(t, noAddr),
		"empty object":     `{}`,
		"invalid JSON":     `{"agent_id":`,
		"wrong type":       `{"agent_id":1,"address":"http://x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assertError(t, postHeartbeat(h, strp(testKey), body), http.StatusBadRequest)
		})
	}
	if l := reg.List(); len(l) != 0 {
		t.Errorf("invalid heartbeat registered %+v", l)
	}
}

// AC6: a valid heartbeat returns 204, then the agent appears in GET /agents
// and GET /agents/{id}; unknown IDs are 404; an empty registry lists [].
func TestHeartbeatThenAgentsEndpoints(t *testing.T) {
	clk := newFakeClock()
	h, reg := newAgentsTestServer(clk)

	w := doReq(h, http.MethodGet, "/agents", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty GET /agents = %d %q, want 200 []", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	containers := []shared.ContainerInfo{{ServerID: "s1", ContainerID: "c1", State: "running"}}
	w = postHeartbeat(h, strp(testKey), hbJSON(t, hbFor("a", containers...)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("heartbeat status = %d body %s, want 204", w.Code, w.Body)
	}
	if w.Body.Len() != 0 {
		t.Errorf("204 with body %q", w.Body)
	}
	postHeartbeat(h, strp(testKey), hbJSON(t, hbFor("0-first")))

	want := shared.Agent{
		ID: "a", Hostname: "host-a", Address: "http://a:8000", Version: "v1",
		Status: shared.AgentHealthy, LastSeen: clk.Now(), Containers: containers,
	}

	w = doReq(h, http.MethodGet, "/agents", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var list []shared.Agent
	singleJSON(t, w.Body.Bytes(), &list)
	if len(list) != 2 || list[0].ID != "0-first" || !reflect.DeepEqual(list[1], want) {
		t.Fatalf("GET /agents = %+v\nwant [0-first, %+v]", list, want)
	}

	// Wire field names.
	var raw []map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &raw)
	for _, k := range []string{"id", "hostname", "address", "version", "status", "last_seen", "containers"} {
		if _, ok := raw[1][k]; !ok {
			t.Errorf("agent JSON missing %q: %s", k, w.Body)
		}
	}
	if got := string(raw[0]["containers"]); got != "[]" {
		t.Errorf("agent with no containers encodes containers as %s, want []", got)
	}

	w = doReq(h, http.MethodGet, "/agents/a", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents/a = %d", w.Code)
	}
	var one shared.Agent
	singleJSON(t, w.Body.Bytes(), &one)
	if !reflect.DeepEqual(one, want) {
		t.Errorf("GET /agents/a = %+v, want %+v", one, want)
	}

	assertError(t, doReq(h, http.MethodGet, "/agents/unknown", ""), http.StatusNotFound)

	// Health is visible through the API after a sweep, and a new heartbeat
	// restores it.
	clk.Advance(91 * time.Second)
	reg.Sweep()
	singleJSON(t, doReq(h, http.MethodGet, "/agents/a", "").Body.Bytes(), &one)
	if one.Status != shared.AgentOffline {
		t.Errorf("after +91s sweep: status %s, want offline", one.Status)
	}
	if w := postHeartbeat(h, strp(testKey), hbJSON(t, hbFor("a"))); w.Code != http.StatusNoContent {
		t.Fatalf("re-heartbeat = %d", w.Code)
	}
	singleJSON(t, doReq(h, http.MethodGet, "/agents/a", "").Body.Bytes(), &one)
	if one.Status != shared.AgentHealthy || !one.LastSeen.Equal(clk.Now()) {
		t.Errorf("after re-heartbeat: %+v, want healthy at %v", one, clk.Now())
	}
}

// Wrong methods on the agent routes get 405.
func TestAgentsRouting(t *testing.T) {
	h, _ := newAgentsTestServer(newFakeClock())
	if w := doReq(h, http.MethodPut, "/agents/heartbeat", "{}"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /agents/heartbeat = %d, want 405", w.Code)
	}
	if w := doReq(h, http.MethodDelete, "/agents", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /agents = %d, want 405", w.Code)
	}
}

// AC7: concurrent heartbeats, GETs and sweeps through the HTTP layer under
// -race.
func TestAgentsConcurrentHTTP(t *testing.T) {
	clk := newFakeClock()
	h, reg := newAgentsTestServer(clk)
	const goroutines = 60
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id := fmt.Sprintf("agent-%d", g%6)
			for i := 0; i < 50; i++ {
				switch g % 4 {
				case 0, 1:
					body := hbJSON(t, hbFor(id, shared.ContainerInfo{ServerID: "s", ContainerID: fmt.Sprint(i), State: "running"}))
					if w := postHeartbeat(h, strp(testKey), body); w.Code != http.StatusNoContent {
						t.Errorf("heartbeat = %d", w.Code)
						return
					}
				case 2:
					if w := doReq(h, http.MethodGet, "/agents", ""); w.Code != http.StatusOK {
						t.Errorf("GET /agents = %d", w.Code)
						return
					}
					doReq(h, http.MethodGet, "/agents/"+id, "")
				case 3:
					clk.Advance(time.Second)
					reg.Sweep()
				}
			}
		}(g)
	}
	wg.Wait()
	if n := len(reg.List()); n != 6 {
		t.Errorf("registry has %d agents, want 6", n)
	}
}

// AC5: GET /agents/{id} shows containers_error after a heartbeat that
// carried one; a later clean heartbeat clears it (key omitted).
func TestAgentContainersErrorRoundTrip(t *testing.T) {
	h, _ := newAgentsTestServer(newFakeClock())
	bad := hbFor("a")
	bad.Containers = []shared.ContainerInfo{}
	bad.ContainersError = "docker ps: context deadline exceeded"
	if w := postHeartbeat(h, strp(testKey), hbJSON(t, bad)); w.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d %s", w.Code, w.Body)
	}
	getRaw := func() map[string]json.RawMessage {
		t.Helper()
		w := doReq(h, http.MethodGet, "/agents/a", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /agents/a = %d", w.Code)
		}
		var raw map[string]json.RawMessage
		singleJSON(t, w.Body.Bytes(), &raw)
		return raw
	}
	raw := getRaw()
	if got := string(raw["containers_error"]); got != `"docker ps: context deadline exceeded"` {
		t.Errorf("containers_error = %s, want the heartbeat's error", got)
	}
	if got := string(raw["containers"]); got != "[]" {
		t.Errorf("containers = %s, want []", got)
	}
	var list []shared.Agent
	singleJSON(t, doReq(h, http.MethodGet, "/agents", "").Body.Bytes(), &list)
	if len(list) != 1 || list[0].ContainersError != bad.ContainersError {
		t.Errorf("GET /agents = %+v, want containers_error set", list)
	}

	good := hbFor("a", shared.ContainerInfo{ServerID: "s1", ContainerID: "c1", State: "running"})
	if w := postHeartbeat(h, strp(testKey), hbJSON(t, good)); w.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", w.Code)
	}
	raw = getRaw()
	if v, ok := raw["containers_error"]; ok {
		t.Errorf("containers_error = %s after a clean heartbeat, want key omitted", v)
	}
}
