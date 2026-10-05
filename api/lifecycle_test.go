package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"snivur/v0/shared"
)

type launchCall struct {
	ctx     context.Context
	baseURL string
	req     shared.LaunchRequest
}

type launchResult struct {
	resp shared.LaunchResponse
	err  error
}

type stopCall struct {
	ctx     context.Context
	baseURL string
	id      string
}

// chanAgent is a fake AgentClient whose Launch and Stop block until the test
// releases them, letting the test step a server through its states.
type chanAgent struct {
	launches      chan launchCall
	launchResults chan launchResult
	stops         chan stopCall
	stopResults   chan error
}

func newChanAgent() *chanAgent {
	return &chanAgent{
		launches:      make(chan launchCall, 1),
		launchResults: make(chan launchResult),
		stops:         make(chan stopCall, 1),
		stopResults:   make(chan error),
	}
}

func (a *chanAgent) Launch(ctx context.Context, baseURL string, req shared.LaunchRequest) (shared.LaunchResponse, error) {
	a.launches <- launchCall{ctx, baseURL, req}
	select {
	case r := <-a.launchResults:
		return r.resp, r.err
	case <-ctx.Done():
		return shared.LaunchResponse{}, ctx.Err()
	}
}

func (a *chanAgent) Stop(ctx context.Context, baseURL, id string) error {
	a.stops <- stopCall{ctx, baseURL, id}
	select {
	case err := <-a.stopResults:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// instantAgent is a fake AgentClient that succeeds immediately.
type instantAgent struct{ launches, stops atomic.Int64 }

func (a *instantAgent) Launch(_ context.Context, _ string, req shared.LaunchRequest) (shared.LaunchResponse, error) {
	a.launches.Add(1)
	return shared.LaunchResponse{ServerID: req.ServerID, ContainerID: "c-" + req.ServerID}, nil
}

func (a *instantAgent) Stop(context.Context, string, string) error {
	a.stops.Add(1)
	return nil
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func send[T any](t *testing.T, ch chan<- T, v T, what string) {
	t.Helper()
	select {
	case ch <- v:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out sending %s", what)
	}
}

// testAgentID is the agent every test server is bound to; testAgentAddr is
// its registry Address when the test uses a fake AgentClient.
const (
	testAgentID   = "agent-a"
	testAgentAddr = "http://agent-a:8000"
)

// healthyRegistry returns a registry holding one healthy agent, testAgentID,
// whose Address is addr.
func healthyRegistry(addr string) *Registry {
	r := NewRegistry(nil, HealthThresholds{})
	r.Upsert(shared.Heartbeat{AgentID: testAgentID, Address: addr})
	return r
}

func createBody(name string) string {
	return fmt.Sprintf(`{"agent_id":%q,"name":%q,"game":"minecraft","config":{"image":"alpine","port":"25565"}}`, testAgentID, name)
}

func doReq(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	h.ServeHTTP(w, r)
	return w
}

func mustCreate(t *testing.T, h http.Handler, name string) shared.Server {
	t.Helper()
	w := doReq(h, http.MethodPost, "/servers", createBody(name))
	if w.Code != http.StatusAccepted {
		t.Fatalf("create %q: status %d body %s", name, w.Code, w.Body)
	}
	var srv shared.Server
	singleJSON(t, w.Body.Bytes(), &srv)
	return srv
}

func getSrv(t *testing.T, h http.Handler, id string) shared.Server {
	t.Helper()
	w := doReq(h, http.MethodGet, "/servers/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d body %s", id, w.Code, w.Body)
	}
	var srv shared.Server
	singleJSON(t, w.Body.Bytes(), &srv)
	return srv
}

// waitState polls GET /servers/{id} until it reports want, with a deadline.
func waitState(t *testing.T, h http.Handler, id string, want shared.ServerState) shared.Server {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last shared.Server
	for time.Now().Before(deadline) {
		last = getSrv(t, h, id)
		if last.State == want {
			return last
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("server %s stuck in %s (message %q), want %s", id, last.State, last.Message, want)
	return last
}

func assertError(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, status, w.Body)
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

// AC2: POST /servers returns 202 + Location within 100ms while the agent
// blocks; the server then moves pending -> starting -> running with its
// container_id set.
func TestCreateServerAsyncLifecycle(t *testing.T) {
	agent := newChanAgent()
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))

	start := time.Now()
	w := doReq(h, http.MethodPost, "/servers", createBody("mc1"))
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Errorf("POST /servers took %v, want < 100ms", elapsed)
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var created shared.Server
	singleJSON(t, w.Body.Bytes(), &created)
	if created.State != shared.StatePending {
		t.Errorf("response state = %s, want pending", created.State)
	}
	if loc := w.Header().Get("Location"); loc != "/servers/"+created.ID || created.ID == "" {
		t.Errorf("Location = %q, want /servers/%s", loc, created.ID)
	}
	if created.Name != "mc1" || created.Game != "minecraft" || created.Config["image"] != "alpine" {
		t.Errorf("response fields: %+v", created)
	}

	call := recv(t, agent.launches, "Launch call")
	waitState(t, h, created.ID, shared.StateStarting)
	if call.req.ServerID != created.ID || call.req.Name != "mc1" || call.req.Game != "minecraft" ||
		call.req.Config["image"] != "alpine" || call.req.Config["port"] != "25565" {
		t.Errorf("LaunchRequest = %+v", call.req)
	}
	// Background context with a 5 minute timeout.
	dl, ok := call.ctx.Deadline()
	if !ok {
		t.Error("launch context has no deadline")
	} else if d := time.Until(dl); d < 4*time.Minute || d > 5*time.Minute {
		t.Errorf("launch deadline in %v, want ~5m", d)
	}

	send(t, agent.launchResults, launchResult{resp: shared.LaunchResponse{ServerID: created.ID, ContainerID: "abc123"}}, "launch result")
	got := waitState(t, h, created.ID, shared.StateRunning)
	if got.ContainerID != "abc123" {
		t.Errorf("container_id = %q, want abc123", got.ContainerID)
	}
	if got.Message != "" {
		t.Errorf("message = %q, want empty", got.Message)
	}
	if !got.UpdatedAt.After(got.CreatedAt) && !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("UpdatedAt %v before CreatedAt %v", got.UpdatedAt, got.CreatedAt)
	}
	// Wire format check: container_id key is present.
	w = doReq(h, http.MethodGet, "/servers/"+created.ID, "")
	var raw map[string]any
	singleJSON(t, w.Body.Bytes(), &raw)
	if raw["container_id"] != "abc123" || raw["state"] != "running" {
		t.Errorf("wire JSON = %v", raw)
	}
}

// AC2 (hardening): a client that disconnects does not cancel the launch.
func TestCreateServerLaunchSurvivesClientCancel(t *testing.T) {
	agent := newChanAgent()
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/servers", strings.NewReader(createBody("mc1"))).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cancel()
	var srv shared.Server
	singleJSON(t, w.Body.Bytes(), &srv)
	call := recv(t, agent.launches, "Launch call")
	if err := call.ctx.Err(); err != nil {
		t.Fatalf("launch context canceled with request: %v", err)
	}
	send(t, agent.launchResults, launchResult{resp: shared.LaunchResponse{ContainerID: "c"}}, "launch result")
	waitState(t, h, srv.ID, shared.StateRunning)
}

// AC3: a launch error ends in failed with the error in message.
func TestCreateServerLaunchFailure(t *testing.T) {
	agent := newChanAgent()
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))
	srv := mustCreate(t, h, "mc1")
	recv(t, agent.launches, "Launch call")
	send(t, agent.launchResults, launchResult{err: errors.New("agent returned 500: docker run: pull access denied")}, "launch result")
	got := waitState(t, h, srv.ID, shared.StateFailed)
	if !strings.Contains(got.Message, "pull access denied") {
		t.Errorf("message = %q, want launch error", got.Message)
	}
	if got.ContainerID != "" {
		t.Errorf("container_id = %q on failed launch", got.ContainerID)
	}
}

// AC3 end to end through the real HTTP agent client: a 500 from the agent
// becomes failed with the agent's error message.
func TestCreateServerLaunchFailureViaHTTPAgent(t *testing.T) {
	fa := newFakeAgent(t, http.StatusInternalServerError, `{"error":"docker run: no such image"}`)
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(fa.URL), newHTTPAgentClient(testKey, fa.Client()), testKey, testControllerKey))
	srv := mustCreate(t, h, "mc1")
	got := waitState(t, h, srv.ID, shared.StateFailed)
	if !strings.Contains(got.Message, "no such image") {
		t.Errorf("message = %q", got.Message)
	}
}

// runningServer creates a server through the handler and drives it to running.
func runningServer(t *testing.T, h http.Handler, agent *chanAgent, name string) shared.Server {
	t.Helper()
	srv := mustCreate(t, h, name)
	recv(t, agent.launches, "Launch call")
	send(t, agent.launchResults, launchResult{resp: shared.LaunchResponse{ContainerID: "cid-" + name}}, "launch result")
	return waitState(t, h, srv.ID, shared.StateRunning)
}

// AC4: stop on running -> 202 stopping -> stopped.
func TestStopRunningServer(t *testing.T) {
	agent := newChanAgent()
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))
	srv := runningServer(t, h, agent, "mc1")

	start := time.Now()
	w := doReq(h, http.MethodPost, "/servers/"+srv.ID+"/stop", "")
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("stop took %v while agent blocks", d)
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body)
	}
	var resp shared.Server
	singleJSON(t, w.Body.Bytes(), &resp)
	if resp.State != shared.StateStopping {
		t.Errorf("response state = %s, want stopping", resp.State)
	}
	call := recv(t, agent.stops, "Stop call")
	if call.id != srv.ID {
		t.Errorf("Stop(%q), want %q", call.id, srv.ID)
	}
	if dl, ok := call.ctx.Deadline(); !ok {
		t.Error("stop context has no deadline")
	} else if d := time.Until(dl); d < 50*time.Second || d > time.Minute {
		t.Errorf("stop deadline in %v, want ~1m", d)
	}
	if got := getSrv(t, h, srv.ID); got.State != shared.StateStopping {
		t.Errorf("state while agent stops = %s, want stopping", got.State)
	}
	send(t, agent.stopResults, nil, "stop result")
	waitState(t, h, srv.ID, shared.StateStopped)
}

// AC4: stop on every non-running state returns 409; unknown id returns 404.
func TestStopNonRunningConflict(t *testing.T) {
	for _, s := range allStates {
		if s == shared.StateRunning {
			continue
		}
		t.Run(string(s), func(t *testing.T) {
			st := NewStore(nil)
			srv := serverIn(t, st, "mc1", s)
			agent := newChanAgent()
			h := withControllerKey(newControllerServer(st, healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))
			w := doReq(h, http.MethodPost, "/servers/"+srv.ID+"/stop", "")
			assertError(t, w, http.StatusConflict)
			if got, _ := st.Get(srv.ID); got.State != s {
				t.Errorf("state changed to %s", got.State)
			}
			select {
			case c := <-agent.stops:
				t.Errorf("agent Stop called for %s server: %v", s, c.id)
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
	t.Run("unknown id", func(t *testing.T) {
		h := withControllerKey(newControllerServer(NewStore(nil), healthyRegistry(testAgentAddr), newChanAgent(), testKey, testControllerKey))
		assertError(t, doReq(h, http.MethodPost, "/servers/nope/stop", ""), http.StatusNotFound)
	})
}

// AC4: an agent 404 during stop (container already gone) still ends in
// stopped, using the real HTTP agent client.
func TestStopAgent404EndsStopped(t *testing.T) {
	var stopPath atomic.Value
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/launch":
			var req shared.LaunchRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			json.NewEncoder(w).Encode(shared.LaunchResponse{ServerID: req.ServerID, ContainerID: "cid"})
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stopPath.Store(r.Method + " " + r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no container"}`))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer agentSrv.Close()
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(agentSrv.URL), newHTTPAgentClient(testKey, agentSrv.Client()), testKey, testControllerKey))
	srv := mustCreate(t, h, "mc1")
	waitState(t, h, srv.ID, shared.StateRunning)
	if w := doReq(h, http.MethodPost, "/servers/"+srv.ID+"/stop", ""); w.Code != http.StatusAccepted {
		t.Fatalf("stop status %d", w.Code)
	}
	got := waitState(t, h, srv.ID, shared.StateStopped)
	if got.Message != "" {
		t.Errorf("message = %q", got.Message)
	}
	if p, _ := stopPath.Load().(string); p != "POST /servers/"+srv.ID+"/stop" {
		t.Errorf("agent stop request = %q", p)
	}
}

// AC4: an agent error during stop ends in failed with the message.
func TestStopAgentErrorEndsFailed(t *testing.T) {
	agent := newChanAgent()
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))
	srv := runningServer(t, h, agent, "mc1")
	doReq(h, http.MethodPost, "/servers/"+srv.ID+"/stop", "")
	recv(t, agent.stops, "Stop call")
	send(t, agent.stopResults, errors.New("agent returned 500: docker stop: boom"), "stop result")
	got := waitState(t, h, srv.ID, shared.StateFailed)
	if !strings.Contains(got.Message, "boom") {
		t.Errorf("message = %q", got.Message)
	}
}

// AC5: same name as a non-terminal server -> 409; reuse after stopped succeeds.
func TestCreateNameConflictAndReuse(t *testing.T) {
	agent := newChanAgent()
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))

	first := mustCreate(t, h, "mc1")
	assertError(t, doReq(h, http.MethodPost, "/servers", createBody("mc1")), http.StatusConflict) // pending
	recv(t, agent.launches, "Launch call")
	waitState(t, h, first.ID, shared.StateStarting)
	assertError(t, doReq(h, http.MethodPost, "/servers", createBody("mc1")), http.StatusConflict) // starting
	send(t, agent.launchResults, launchResult{resp: shared.LaunchResponse{ContainerID: "c1"}}, "launch result")
	waitState(t, h, first.ID, shared.StateRunning)
	assertError(t, doReq(h, http.MethodPost, "/servers", createBody("mc1")), http.StatusConflict) // running

	doReq(h, http.MethodPost, "/servers/"+first.ID+"/stop", "")
	recv(t, agent.stops, "Stop call")
	assertError(t, doReq(h, http.MethodPost, "/servers", createBody("mc1")), http.StatusConflict) // stopping
	send(t, agent.stopResults, nil, "stop result")
	waitState(t, h, first.ID, shared.StateStopped)

	second := mustCreate(t, h, "mc1")
	if second.ID == first.ID {
		t.Fatal("reused server got the same ID")
	}
	recv(t, agent.launches, "second Launch call")
	send(t, agent.launchResults, launchResult{resp: shared.LaunchResponse{ContainerID: "c2"}}, "launch result")
	waitState(t, h, second.ID, shared.StateRunning)
	if got := getSrv(t, h, first.ID); got.State != shared.StateStopped {
		t.Errorf("first server state = %s, want stopped", got.State)
	}
}

func TestListAndGetEndpoints(t *testing.T) {
	agent := &instantAgent{}
	h := withControllerKey(newControllerServer(NewStore(time.Now), healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))

	w := doReq(h, http.MethodGet, "/servers", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty list: status %d body %q, want 200 []", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	a := mustCreate(t, h, "a")
	b := mustCreate(t, h, "b")
	w = doReq(h, http.MethodGet, "/servers", "")
	var list []shared.Server
	singleJSON(t, w.Body.Bytes(), &list)
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Errorf("list = %+v, want [a b]", list)
	}
	assertError(t, doReq(h, http.MethodGet, "/servers/nope", ""), http.StatusNotFound)
}

// AC6: concurrent create, stop and list from 60+ goroutines under -race.
func TestConcurrentCreateStopList(t *testing.T) {
	agent := &instantAgent{}
	st := NewStore(time.Now)
	h := withControllerKey(newControllerServer(st, healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))

	const lifecycles = 40
	const listers = 20
	const sameStoppers = 20

	// One shared running server that many goroutines race to stop.
	shared0 := mustCreate(t, h, "shared")
	waitState(t, h, shared0.ID, shared.StateRunning)

	var wg sync.WaitGroup
	var stopAccepted atomic.Int64
	errs := make(chan error, lifecycles+listers+sameStoppers)
	startGate := make(chan struct{})

	for i := 0; i < lifecycles; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startGate
			name := fmt.Sprintf("srv-%d", i)
			w := doReq(h, http.MethodPost, "/servers", createBody(name))
			if w.Code != http.StatusAccepted {
				errs <- fmt.Errorf("create %s: %d", name, w.Code)
				return
			}
			var srv shared.Server
			if err := json.Unmarshal(w.Body.Bytes(), &srv); err != nil {
				errs <- err
				return
			}
			// Duplicate name must conflict while non-terminal.
			if w := doReq(h, http.MethodPost, "/servers", createBody(name)); w.Code != http.StatusConflict {
				errs <- fmt.Errorf("duplicate %s: %d, want 409", name, w.Code)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				s, _ := st.Get(srv.ID)
				if s.State == shared.StateRunning {
					break
				}
				if time.Now().After(deadline) {
					errs <- fmt.Errorf("%s stuck in %s", name, s.State)
					return
				}
				time.Sleep(time.Millisecond)
			}
			if w := doReq(h, http.MethodPost, "/servers/"+srv.ID+"/stop", ""); w.Code != http.StatusAccepted {
				errs <- fmt.Errorf("stop %s: %d", name, w.Code)
			}
		}(i)
	}
	for i := 0; i < listers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			for j := 0; j < 50; j++ {
				w := doReq(h, http.MethodGet, "/servers", "")
				var list []shared.Server
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
					errs <- fmt.Errorf("list: %d %s", w.Code, w.Body)
					return
				}
				for k := 1; k < len(list); k++ {
					if list[k].CreatedAt.Before(list[k-1].CreatedAt) {
						errs <- errors.New("list not sorted by created_at")
						return
					}
				}
			}
		}()
	}
	for i := 0; i < sameStoppers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			switch w := doReq(h, http.MethodPost, "/servers/"+shared0.ID+"/stop", ""); w.Code {
			case http.StatusAccepted:
				stopAccepted.Add(1)
			case http.StatusConflict:
			default:
				errs <- fmt.Errorf("racing stop: %d", w.Code)
			}
		}()
	}
	close(startGate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := stopAccepted.Load(); n != 1 {
		t.Errorf("%d concurrent stops accepted for one server, want exactly 1", n)
	}

	// Everything drains to stopped.
	deadline := time.Now().Add(5 * time.Second)
	for {
		list := st.List()
		done := len(list) == lifecycles+1
		for _, s := range list {
			if s.State != shared.StateStopped {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not all servers stopped: %+v", list)
		}
		time.Sleep(time.Millisecond)
	}
	if got := agent.stops.Load(); got != lifecycles+1 {
		t.Errorf("agent Stop called %d times, want %d", got, lifecycles+1)
	}
}

// AC8 (controller): body larger than 1 MiB returns 413 with ErrorResponse.
func TestControllerBodyTooLarge(t *testing.T) {
	if maxBodyBytes != 1<<20 {
		t.Errorf("maxBodyBytes = %d, want 1 MiB", maxBodyBytes)
	}
	agent := &instantAgent{}
	h := withControllerKey(newControllerServer(NewStore(nil), healthyRegistry(testAgentAddr), agent, testKey, testControllerKey))
	big := strings.Repeat("a", 1<<20)
	cases := map[string]string{
		"oversized string":           `{"name":"mc1","game":"` + big + `","config":{"image":"alpine"}}`,
		"valid JSON then padding":    validBody + strings.Repeat(" ", 1<<20),
		"oversized config map value": `{"name":"mc1","config":{"image":"alpine","x":"` + big + `"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			assertError(t, post(t, h, body), http.StatusRequestEntityTooLarge)
		})
	}
	if n := agent.launches.Load(); n != 0 {
		t.Errorf("agent launched %d times for oversized bodies", n)
	}
	// Just under the limit is fine.
	pad := (1 << 20) - len(validBody) - 1
	if w := post(t, h, validBody+strings.Repeat(" ", pad)); w.Code != http.StatusAccepted {
		t.Errorf("body under limit: status %d, want 202 (%s)", w.Code, w.Body)
	}
}

func TestControllerHTTPServerHardening(t *testing.T) {
	s := newHTTPServer(":0", http.NotFoundHandler())
	if s.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", s.ReadHeaderTimeout)
	}
	if s.Addr != ":0" || s.Handler == nil {
		t.Errorf("server = %+v", s)
	}
}
