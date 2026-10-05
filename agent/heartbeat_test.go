package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"snivur/v0/shared"
)

// listRuntime is a Runtime whose List result can be changed at any time.
type listRuntime struct {
	mu    sync.Mutex
	list  []shared.ContainerInfo
	err   error
	calls atomic.Int64
}

func (r *listRuntime) Start(context.Context, shared.LaunchRequest) (string, error) {
	return "", errors.New("not implemented")
}
func (r *listRuntime) Stop(context.Context, string) error { return errors.New("not implemented") }
func (r *listRuntime) List(context.Context) ([]shared.ContainerInfo, error) {
	r.calls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.list, r.err
}
func (r *listRuntime) set(list []shared.ContainerInfo, err error) {
	r.mu.Lock()
	r.list, r.err = list, err
	r.mu.Unlock()
}

// hbReq is one request received by the fake controller.
type hbReq struct {
	method, path, key, contentType string
	body                           []byte
	at                             time.Time
}

// fakeController records every heartbeat on a channel and answers with a
// configurable status.
type fakeController struct {
	*httptest.Server
	reqs   chan hbReq
	status atomic.Int64
	count  atomic.Int64
}

func newFakeController(t *testing.T, status int) *fakeController {
	t.Helper()
	fc := &fakeController{reqs: make(chan hbReq, 10000)}
	fc.status.Store(int64(status))
	fc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fc.count.Add(1)
		select {
		case fc.reqs <- hbReq{r.Method, r.URL.Path, r.Header.Get("X-API-Key"), r.Header.Get("Content-Type"), body, time.Now()}:
		default:
		}
		st := int(fc.status.Load())
		if st != http.StatusNoContent {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(st)
			w.Write([]byte(`{"error":"boom"}`))
			return
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(fc.Close)
	return fc
}

// next waits for the next heartbeat request with a deadline.
func (fc *fakeController) next(t *testing.T) hbReq {
	t.Helper()
	select {
	case r := <-fc.reqs:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a heartbeat")
		panic("unreachable")
	}
}

// waitCount waits until at least n heartbeats have been received.
func (fc *fakeController) waitCount(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for fc.count.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("got %d heartbeats, want >= %d", fc.count.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// startHeartbeater runs h in the background and returns a stop function that
// cancels it and asserts Run returns promptly.
func startHeartbeater(t *testing.T, h *Heartbeater) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Run(ctx)
	}()
	var once sync.Once
	stop = func() {
		t.Helper()
		once.Do(func() {
			start := time.Now()
			cancel()
			select {
			case <-done:
				if d := time.Since(start); d > time.Second {
					t.Errorf("Run took %v to return after cancel", d)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return after ctx was cancelled")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

var testIdentity = Identity{AgentID: "agent-1", Hostname: "host-1", Address: "http://10.0.0.5:8000", Version: "v1.2.3"}

// AC4: the first heartbeat is sent immediately (before the first tick), to
// POST /agents/heartbeat, with the API key and the full payload.
func TestHeartbeaterSendsImmediately(t *testing.T) {
	fc := newFakeController(t, http.StatusNoContent)
	containers := []shared.ContainerInfo{
		{ServerID: "s1", ContainerID: "c1", State: "running"},
		{ServerID: "s2", ContainerID: "c2", State: "exited"},
	}
	rt := &listRuntime{list: containers}
	h := &Heartbeater{
		ControllerURL: fc.URL + "/", // trailing slash must not produce //agents
		APIKey:        testKey,
		Interval:      time.Hour, // no tick can fire during the test
		Runtime:       rt,
		Client:        fc.Client(),
		Identity:      testIdentity,
	}
	before := time.Now()
	stop := startHeartbeater(t, h)
	r := fc.next(t)

	if r.method != http.MethodPost || r.path != "/agents/heartbeat" {
		t.Errorf("request = %s %s, want POST /agents/heartbeat", r.method, r.path)
	}
	if r.key != testKey {
		t.Errorf("X-API-Key = %q, want %q", r.key, testKey)
	}
	if !strings.HasPrefix(r.contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", r.contentType)
	}

	// Wire field names, per the spec.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.body, &raw); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", r.body, err)
	}
	for _, k := range []string{"agent_id", "hostname", "address", "version", "containers", "sent_at"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("payload missing %q: %s", k, r.body)
		}
	}
	var cs []map[string]any
	if err := json.Unmarshal(raw["containers"], &cs); err != nil || len(cs) != 2 {
		t.Fatalf("containers = %s (%v)", raw["containers"], err)
	}
	for _, k := range []string{"server_id", "container_id", "state"} {
		if _, ok := cs[0][k]; !ok {
			t.Errorf("container missing %q: %s", k, raw["containers"])
		}
	}

	var hb shared.Heartbeat
	if err := json.Unmarshal(r.body, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.AgentID != "agent-1" || hb.Hostname != "host-1" || hb.Address != "http://10.0.0.5:8000" || hb.Version != "v1.2.3" {
		t.Errorf("identity = %+v", hb)
	}
	if !reflect.DeepEqual(hb.Containers, containers) {
		t.Errorf("containers = %+v, want %+v", hb.Containers, containers)
	}
	if hb.SentAt.Before(before.Add(-time.Second)) || hb.SentAt.After(time.Now().Add(time.Second)) {
		t.Errorf("sent_at = %v, want about now", hb.SentAt)
	}

	stop()
	// With a 1h interval only the immediate heartbeat may have been sent.
	if n := fc.count.Load(); n != 1 {
		t.Errorf("got %d heartbeats with a 1h interval, want exactly 1", n)
	}
}

// AC4: more heartbeats on each tick; Run stops when ctx is cancelled and
// sends nothing afterwards.
func TestHeartbeaterTicksAndStops(t *testing.T) {
	fc := newFakeController(t, http.StatusNoContent)
	rt := &listRuntime{list: []shared.ContainerInfo{}}
	h := &Heartbeater{ControllerURL: fc.URL, APIKey: testKey, Interval: 10 * time.Millisecond, Runtime: rt, Client: fc.Client(), Identity: testIdentity}
	stop := startHeartbeater(t, h)
	fc.waitCount(t, 5)
	for i := 0; i < 5; i++ {
		if r := fc.next(t); r.key != testKey {
			t.Errorf("heartbeat %d: X-API-Key = %q", i, r.key)
		}
	}
	stop()
	n := fc.count.Load()
	time.Sleep(50 * time.Millisecond) // 5 intervals: any leaked ticker would send
	if got := fc.count.Load(); got != n {
		t.Errorf("heartbeats kept arriving after Run returned: %d -> %d", n, got)
	}
}

// AC4: cancel while a request is in flight to a hung controller still makes
// Run return promptly (the request honours ctx).
func TestHeartbeaterStopsDuringHungRequest(t *testing.T) {
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	h := &Heartbeater{ControllerURL: srv.URL, APIKey: testKey, Interval: 10 * time.Millisecond, Runtime: &listRuntime{}, Client: srv.Client(), Identity: testIdentity}
	stop := startHeartbeater(t, h)
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat arrived")
	}
	stop()
}

// AC5: a controller returning 500 does not stop the loop; it keeps sending
// and recovers when the controller does.
func TestHeartbeaterSurvives500(t *testing.T) {
	fc := newFakeController(t, http.StatusInternalServerError)
	h := &Heartbeater{ControllerURL: fc.URL, APIKey: testKey, Interval: 10 * time.Millisecond, Runtime: &listRuntime{}, Client: fc.Client(), Identity: testIdentity}
	stop := startHeartbeater(t, h)
	fc.waitCount(t, 3)
	fc.status.Store(http.StatusNoContent)
	fc.waitCount(t, fc.count.Load()+3)
	stop()
}

// AC5: an unreachable controller does not stop the loop either.
func TestHeartbeaterSurvivesUnreachableController(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	rt := &listRuntime{}
	h := &Heartbeater{ControllerURL: url, APIKey: testKey, Interval: 10 * time.Millisecond, Runtime: rt, Client: &http.Client{Timeout: time.Second}, Identity: testIdentity}
	stop := startHeartbeater(t, h)
	deadline := time.Now().Add(5 * time.Second)
	for rt.calls.Load() < 3 { // each beat lists first, so this counts attempts
		if time.Now().After(deadline) {
			t.Fatalf("only %d attempts against an unreachable controller", rt.calls.Load())
		}
		time.Sleep(time.Millisecond)
	}
	stop()
}

// AC5: a Runtime.List error still sends the heartbeat with containers: []
// (not null) and the loop keeps going; when List recovers, containers are
// reported again.
func TestHeartbeaterSurvivesListError(t *testing.T) {
	fc := newFakeController(t, http.StatusNoContent)
	rt := &listRuntime{}
	rt.set(nil, errors.New("docker daemon down"))
	h := &Heartbeater{ControllerURL: fc.URL, APIKey: testKey, Interval: 10 * time.Millisecond, Runtime: rt, Client: fc.Client(), Identity: testIdentity}
	stop := startHeartbeater(t, h)
	for i := 0; i < 3; i++ {
		r := fc.next(t)
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(r.body, &raw); err != nil {
			t.Fatal(err)
		}
		if got := string(raw["containers"]); got != "[]" {
			t.Fatalf("heartbeat %d during List error: containers = %s, want []", i, got)
		}
		if string(raw["agent_id"]) != `"agent-1"` {
			t.Errorf("agent_id = %s", raw["agent_id"])
		}
	}
	want := []shared.ContainerInfo{{ServerID: "s1", ContainerID: "c1", State: "running"}}
	rt.set(want, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := fc.next(t)
		var hb shared.Heartbeat
		if err := json.Unmarshal(r.body, &hb); err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(hb.Containers, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("containers never recovered: %+v", hb.Containers)
		}
	}
	stop()
}

// A List that returns (nil, nil) is also sent as [] rather than null.
func TestHeartbeaterNilListSendsEmptyArray(t *testing.T) {
	fc := newFakeController(t, http.StatusNoContent)
	h := &Heartbeater{ControllerURL: fc.URL, APIKey: testKey, Interval: time.Hour, Runtime: &listRuntime{}, Client: fc.Client(), Identity: testIdentity}
	stop := startHeartbeater(t, h)
	r := fc.next(t)
	stop()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.body, &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw["containers"]); got != "[]" {
		t.Errorf("containers = %s, want []", got)
	}
}

// Spec: the heartbeat HTTP client timeout is 5s; Version defaults to "dev".
func TestHeartbeatClientAndVersionDefaults(t *testing.T) {
	if got := newHeartbeatClient().Timeout; got != 5*time.Second {
		t.Errorf("heartbeat client timeout = %v, want 5s", got)
	}
	if Version != "dev" {
		t.Errorf("Version = %q, want dev", Version)
	}
}

// Spec (Environment): new agent config variables and their defaults.
func TestAgentLoadConfigHeartbeat(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("hostname unavailable: %v", err)
	}
	t.Run("defaults", func(t *testing.T) {
		cfg, err := loadConfig(func(k string) string { return map[string]string{"SNIVUR_AGENT_API_KEY": "k"}[k] })
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AgentID != host {
			t.Errorf("AgentID = %q, want hostname %q", cfg.AgentID, host)
		}
		if cfg.ControllerURL != "" {
			t.Errorf("ControllerURL = %q, want empty (heartbeats disabled)", cfg.ControllerURL)
		}
		if cfg.AdvertiseURL != "http://localhost:8000" {
			t.Errorf("AdvertiseURL = %q, want http://localhost:8000", cfg.AdvertiseURL)
		}
		if cfg.HeartbeatInterval != 10*time.Second {
			t.Errorf("HeartbeatInterval = %v, want 10s", cfg.HeartbeatInterval)
		}
	})
	t.Run("advertise derived from addr port", func(t *testing.T) {
		cfg, err := loadConfig(func(k string) string {
			return map[string]string{"SNIVUR_AGENT_API_KEY": "k", "SNIVUR_AGENT_ADDR": "0.0.0.0:9123"}[k]
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AdvertiseURL != "http://localhost:9123" {
			t.Errorf("AdvertiseURL = %q, want http://localhost:9123", cfg.AdvertiseURL)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		env := map[string]string{
			"SNIVUR_AGENT_API_KEY":       "k",
			"SNIVUR_AGENT_ID":            "edge-7",
			"SNIVUR_CONTROLLER_URL":      "http://ctrl:8080",
			"SNIVUR_AGENT_ADVERTISE_URL": "http://10.1.2.3:8000",
			"SNIVUR_HEARTBEAT_INTERVAL":  "1500ms",
		}
		cfg, err := loadConfig(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AgentID != "edge-7" || cfg.ControllerURL != "http://ctrl:8080" ||
			cfg.AdvertiseURL != "http://10.1.2.3:8000" || cfg.HeartbeatInterval != 1500*time.Millisecond {
			t.Errorf("cfg = %+v", cfg)
		}
	})
	for _, bad := range []string{"abc", "10", "5 s", "0s", "-1s"} {
		t.Run("invalid interval "+bad, func(t *testing.T) {
			_, err := loadConfig(func(k string) string {
				return map[string]string{"SNIVUR_AGENT_API_KEY": "k", "SNIVUR_HEARTBEAT_INTERVAL": bad}[k]
			})
			if err == nil {
				t.Fatalf("SNIVUR_HEARTBEAT_INTERVAL=%q accepted, want error", bad)
			}
			if !strings.Contains(err.Error(), "SNIVUR_HEARTBEAT_INTERVAL") {
				t.Errorf("error %q should name the variable", err)
			}
		})
	}
}

// decodeRaw decodes a heartbeat body into its raw JSON fields.
func decodeRaw(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", body, err)
	}
	return raw
}

// setListTimeout shortens listTimeout for one test. Call it before
// startHeartbeater so the restore runs after Run has returned (Cleanup is
// LIFO), keeping the package var race-free.
func setListTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := listTimeout
	listTimeout = d
	t.Cleanup(func() { listTimeout = old })
}

// AC5: a List error sends "containers":[] plus a non-empty containers_error
// carrying the error text.
func TestHeartbeatListErrorSetsContainersError(t *testing.T) {
	fc := newFakeController(t, http.StatusNoContent)
	rt := &listRuntime{}
	rt.set([]shared.ContainerInfo{{ServerID: "stale", ContainerID: "c", State: "running"}}, errors.New("docker daemon down"))
	h := &Heartbeater{ControllerURL: fc.URL, APIKey: testKey, Interval: time.Hour, Runtime: rt, Client: fc.Client(), Identity: testIdentity}
	stop := startHeartbeater(t, h)
	raw := decodeRaw(t, fc.next(t).body)
	stop()
	if got := string(raw["containers"]); got != "[]" {
		t.Errorf("containers = %s, want [] (partial results must not be sent on error)", got)
	}
	var msg string
	if err := json.Unmarshal(raw["containers_error"], &msg); err != nil || msg == "" {
		t.Fatalf("containers_error = %s (%v), want non-empty string", raw["containers_error"], err)
	}
	if !strings.Contains(msg, "docker daemon down") {
		t.Errorf("containers_error = %q, want it to contain the List error", msg)
	}
}

// AC5: a successful List (including an empty one) omits the containers_error
// key entirely.
func TestHeartbeatListSuccessOmitsContainersError(t *testing.T) {
	for name, list := range map[string][]shared.ContainerInfo{
		"with containers": {{ServerID: "s1", ContainerID: "c1", State: "running"}},
		"empty":           {},
		"nil":             nil,
	} {
		t.Run(name, func(t *testing.T) {
			fc := newFakeController(t, http.StatusNoContent)
			h := &Heartbeater{ControllerURL: fc.URL, APIKey: testKey, Interval: time.Hour, Runtime: &listRuntime{list: list}, Client: fc.Client(), Identity: testIdentity}
			stop := startHeartbeater(t, h)
			r := fc.next(t)
			stop()
			raw := decodeRaw(t, r.body)
			if v, ok := raw["containers_error"]; ok {
				t.Errorf("containers_error present (%s) on success: %s", v, r.body)
			}
			if string(raw["containers"]) == "null" {
				t.Errorf("containers = null")
			}
		})
	}
}

// blockingRuntime's List blocks until its context is done while block is
// set; otherwise it returns list immediately.
type blockingRuntime struct {
	listRuntime
	block    atomic.Bool
	ctxEnded atomic.Int64 // List calls that ended because their ctx was done
}

func (r *blockingRuntime) List(ctx context.Context) ([]shared.ContainerInfo, error) {
	if r.block.Load() {
		<-ctx.Done()
		r.ctxEnded.Add(1)
		return nil, ctx.Err()
	}
	return r.listRuntime.List(ctx)
}

// AC5: a List that blocks until its ctx is cancelled is cut off by
// listTimeout; the heartbeat still arrives promptly with containers_error,
// later heartbeats keep flowing, and recovery clears the error.
func TestHeartbeatListTimeout(t *testing.T) {
	setListTimeout(t, 20*time.Millisecond)
	fc := newFakeController(t, http.StatusNoContent)
	rt := &blockingRuntime{}
	rt.block.Store(true)
	h := &Heartbeater{ControllerURL: fc.URL, APIKey: testKey, Interval: 10 * time.Millisecond, Runtime: rt, Client: fc.Client(), Identity: testIdentity}
	start := time.Now()
	stop := startHeartbeater(t, h)

	first := fc.next(t)
	if d := first.at.Sub(start); d > time.Second {
		t.Errorf("first heartbeat took %v with a 20ms listTimeout; List was not cut off promptly", d)
	}
	for i := 0; i < 3; i++ {
		r := first
		if i > 0 {
			r = fc.next(t)
		}
		raw := decodeRaw(t, r.body)
		if got := string(raw["containers"]); got != "[]" {
			t.Errorf("heartbeat %d: containers = %s, want []", i, got)
		}
		var msg string
		if json.Unmarshal(raw["containers_error"], &msg); msg == "" {
			t.Errorf("heartbeat %d: containers_error missing: %s", i, r.body)
		}
	}
	if rt.ctxEnded.Load() < 3 {
		t.Errorf("List ctx ended %d times, want >= 3", rt.ctxEnded.Load())
	}

	// Recovery: once List works, containers come back and the error key goes.
	want := []shared.ContainerInfo{{ServerID: "s1", ContainerID: "c1", State: "running"}}
	rt.set(want, nil)
	rt.block.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		body := fc.next(t).body
		raw := decodeRaw(t, body)
		var hb shared.Heartbeat
		if err := json.Unmarshal(body, &hb); err != nil {
			t.Fatal(err)
		}
		if _, hasErr := raw["containers_error"]; !hasErr && reflect.DeepEqual(hb.Containers, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeats never recovered: %+v", hb)
		}
	}
	stop()
}

// AC5: a List that ignores its ctx and returns success only after the
// deadline is treated as a failure (its result is stale).
type slowRuntime struct{ listRuntime }

func (r *slowRuntime) List(context.Context) ([]shared.ContainerInfo, error) {
	time.Sleep(4 * listTimeoutForSlow)
	return []shared.ContainerInfo{{ServerID: "late", ContainerID: "c", State: "running"}}, nil
}

const listTimeoutForSlow = 10 * time.Millisecond

func TestHeartbeatListPastDeadlineIsFailure(t *testing.T) {
	setListTimeout(t, listTimeoutForSlow)
	fc := newFakeController(t, http.StatusNoContent)
	h := &Heartbeater{ControllerURL: fc.URL, APIKey: testKey, Interval: time.Hour, Runtime: &slowRuntime{}, Client: fc.Client(), Identity: testIdentity}
	stop := startHeartbeater(t, h)
	raw := decodeRaw(t, fc.next(t).body)
	stop()
	if got := string(raw["containers"]); got != "[]" {
		t.Errorf("containers = %s, want [] for a List that returned after its deadline", got)
	}
	if _, ok := raw["containers_error"]; !ok {
		t.Error("containers_error missing for a List that returned after its deadline")
	}
}

// Spec: listTimeout defaults to 5s.
func TestListTimeoutDefault(t *testing.T) {
	if listTimeout != 5*time.Second {
		t.Errorf("listTimeout = %v, want 5s", listTimeout)
	}
}
