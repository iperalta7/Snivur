package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"snivur/v0/shared"
)

// End-to-end ADR 0004 tests: HTTP heartbeats -> Reconcile -> store, with a
// fake clock driving both the store and the registry.

type e2e struct {
	t     *testing.T
	clk   *fakeClock
	store *Store
	reg   *Registry
	h     http.Handler
}

func newE2E(t *testing.T, agents AgentClient) *e2e {
	clk := newFakeClock()
	st := NewStore(clk.Now)
	reg := NewRegistry(clk.Now, HealthThresholds{})
	return &e2e{t: t, clk: clk, store: st, reg: reg, h: withControllerKey(newControllerServer(st, reg, agents, testKey, testControllerKey))}
}

// beat posts a heartbeat from agent id reporting the given containers.
func (e *e2e) beat(id string, cs ...shared.ContainerInfo) {
	e.t.Helper()
	hb := hbFor(id, cs...)
	if hb.Containers == nil {
		hb.Containers = []shared.ContainerInfo{}
	}
	e.beatHB(hb)
}

func (e *e2e) beatHB(hb shared.Heartbeat) {
	e.t.Helper()
	if w := postHeartbeat(e.h, strp(testKey), hbJSON(e.t, hb)); w.Code != http.StatusNoContent {
		e.t.Fatalf("heartbeat %s: status %d body %s", hb.AgentID, w.Code, w.Body)
	}
}

func (e *e2e) create(agentID, name string) shared.Server {
	e.t.Helper()
	body := fmt.Sprintf(`{"agent_id":%q,"name":%q,"game":"minecraft","config":{"image":"alpine"}}`, agentID, name)
	w := doReq(e.h, http.MethodPost, "/servers", body)
	if w.Code != http.StatusAccepted {
		e.t.Fatalf("create %s on %s: %d %s", name, agentID, w.Code, w.Body)
	}
	var srv shared.Server
	singleJSON(e.t, w.Body.Bytes(), &srv)
	if srv.AgentID != agentID {
		e.t.Errorf("created server agent_id = %q, want %q", srv.AgentID, agentID)
	}
	return srv
}

func (e *e2e) state(id string) shared.Server {
	e.t.Helper()
	s, ok := e.store.Get(id)
	if !ok {
		e.t.Fatalf("server %s missing", id)
	}
	return s
}

func (e *e2e) expect(id string, want shared.ServerState, msg string) {
	e.t.Helper()
	s := e.state(id)
	if s.State != want || (msg != "" && s.Message != msg) {
		e.t.Fatalf("server %s = %s (%q), want %s (%q)", id, s.State, s.Message, want, msg)
	}
}

func (e *e2e) sweep() { handleStatusChanges(e.reg.Sweep(), e.store) }

func running(id string) shared.ContainerInfo {
	return shared.ContainerInfo{ServerID: id, ContainerID: "c-" + id, State: "running"}
}

// runningOn registers agent, creates a server on it and waits for running.
func (e *e2e) runningOn(agentID, name string) shared.Server {
	e.t.Helper()
	srv := e.create(agentID, name)
	return waitState(e.t, e.h, srv.ID, shared.StateRunning)
}

// AC2: create on A -> running; a heartbeat from A without the container
// (after the grace period) -> failed.
func TestE2EMissingContainerFails(t *testing.T) {
	e := newE2E(t, &instantAgent{})
	e.beat("a")
	srv := e.runningOn("a", "mc1")

	e.clk.Advance(reconcileGrace + time.Second)
	e.beat("a", running(srv.ID))
	e.expect(srv.ID, shared.StateRunning, "")

	e.beat("a", shared.ContainerInfo{ServerID: srv.ID, ContainerID: "c", State: "exited"})
	e.expect(srv.ID, shared.StateFailed, "container not running on agent")
}

// AC2 variant: container simply absent from the list.
func TestE2EAbsentContainerFails(t *testing.T) {
	e := newE2E(t, &instantAgent{})
	e.beat("a")
	srv := e.runningOn("a", "mc1")
	e.clk.Advance(reconcileGrace + time.Second)
	e.beat("a")
	e.expect(srv.ID, shared.StateFailed, "container not running on agent")
}

// AC6: grace period through HTTP: a heartbeat lacking the container within
// reconcileGrace (including exactly at it) does nothing; after it, failed.
func TestE2EGracePeriod(t *testing.T) {
	e := newE2E(t, &instantAgent{})
	e.beat("a")
	srv := e.runningOn("a", "mc1")

	e.beat("a") // immediately after launch
	e.expect(srv.ID, shared.StateRunning, "")
	e.clk.Advance(reconcileGrace - time.Second)
	e.beat("a")
	e.expect(srv.ID, shared.StateRunning, "")
	e.clk.Advance(time.Second) // exactly reconcileGrace
	e.beat("a")
	e.expect(srv.ID, shared.StateRunning, "")
	e.clk.Advance(time.Nanosecond)
	e.beat("a")
	e.expect(srv.ID, shared.StateFailed, "container not running on agent")
}

// AC3: running on A -> A offline in a sweep -> unknown; heartbeat with the
// container -> running; later heartbeat without it -> failed. Servers on B,
// which keeps heartbeating, are untouched.
func TestE2EOfflineUnknownRecoverThenFail(t *testing.T) {
	e := newE2E(t, &instantAgent{})
	e.beat("a")
	e.beat("b")
	sa := e.runningOn("a", "on-a")
	sb := e.runningOn("b", "on-b")

	// A goes silent; B keeps heartbeating with its container.
	for _, step := range []time.Duration{30 * time.Second, 30 * time.Second, 30 * time.Second} {
		e.clk.Advance(step)
		e.beat("b", running(sb.ID))
		e.sweep()
		e.expect(sa.ID, shared.StateRunning, "") // healthy/unhealthy: no change
	}
	if a := mustGet(t, e.reg, "a"); a.Status != shared.AgentUnhealthy {
		t.Fatalf("agent a = %s at 90s, want unhealthy", a.Status)
	}
	e.clk.Advance(time.Second)
	e.beat("b", running(sb.ID))
	e.sweep()
	if a := mustGet(t, e.reg, "a"); a.Status != shared.AgentOffline {
		t.Fatalf("agent a = %s at 91s, want offline", a.Status)
	}
	e.expect(sa.ID, shared.StateUnknown, "agent offline")
	e.expect(sb.ID, shared.StateRunning, "")

	// A second sweep at the same time changes nothing.
	e.sweep()
	e.expect(sa.ID, shared.StateUnknown, "agent offline")

	// A comes back with the container.
	e.beat("a", running(sa.ID))
	e.expect(sa.ID, shared.StateRunning, "agent recovered")
	if a := mustGet(t, e.reg, "a"); a.Status != shared.AgentHealthy {
		t.Errorf("agent a = %s after heartbeat", a.Status)
	}

	// Later heartbeat (past grace) without it -> failed.
	e.clk.Advance(reconcileGrace + time.Second)
	e.beat("a")
	e.expect(sa.ID, shared.StateFailed, "container not running on agent")
	e.expect(sb.ID, shared.StateRunning, "")
}

// AC3 variant: unknown -> heartbeat without the container -> failed.
func TestE2EOfflineUnknownThenMissingFails(t *testing.T) {
	e := newE2E(t, &instantAgent{})
	e.beat("a")
	sa := e.runningOn("a", "on-a")
	e.clk.Advance(defaultOfflineAfter + time.Second)
	e.sweep()
	e.expect(sa.ID, shared.StateUnknown, "agent offline")
	e.beat("a")
	e.expect(sa.ID, shared.StateFailed, "container not found after agent recovered")
}

// AC6: a sweep marks the agent offline just before its heartbeat; the
// servers sit in unknown while the agent is healthy (a containers_error
// heartbeat does not reconcile), and the next good heartbeat restores them.
func TestE2EUnknownWhileHealthyRecovers(t *testing.T) {
	e := newE2E(t, &instantAgent{})
	e.beat("a")
	s1 := e.runningOn("a", "s1")
	s2 := e.runningOn("a", "s2")
	e.clk.Advance(defaultOfflineAfter + time.Millisecond)
	e.sweep()
	e.expect(s1.ID, shared.StateUnknown, "")
	e.expect(s2.ID, shared.StateUnknown, "")

	hb := hbFor("a")
	hb.Containers = []shared.ContainerInfo{}
	hb.ContainersError = "docker ps timed out"
	e.beatHB(hb)
	if a := mustGet(t, e.reg, "a"); a.Status != shared.AgentHealthy {
		t.Fatalf("agent = %s, want healthy", a.Status)
	}
	e.expect(s1.ID, shared.StateUnknown, "")
	e.expect(s2.ID, shared.StateUnknown, "")

	e.clk.Advance(time.Second)
	e.beat("a", running(s1.ID), running(s2.ID))
	e.expect(s1.ID, shared.StateRunning, "agent recovered")
	e.expect(s2.ID, shared.StateRunning, "agent recovered")
}

// AC6: a heartbeat during starting changes nothing.
func TestE2EHeartbeatDuringStarting(t *testing.T) {
	agent := newChanAgent()
	e := newE2E(t, agent)
	e.beat("a")
	srv := e.create("a", "mc1")
	recv(t, agent.launches, "Launch call")
	waitState(t, e.h, srv.ID, shared.StateStarting)

	e.clk.Advance(time.Hour) // far past grace; also keep A alive
	e.beat("a")
	e.expect(srv.ID, shared.StateStarting, "")
	e.beat("a", shared.ContainerInfo{ServerID: srv.ID, State: "exited"})
	e.expect(srv.ID, shared.StateStarting, "")
	e.beat("a", running(srv.ID))
	e.expect(srv.ID, shared.StateStarting, "")

	send(t, agent.launchResults, launchResult{resp: shared.LaunchResponse{ContainerID: "c"}}, "launch result")
	waitState(t, e.h, srv.ID, shared.StateRunning)
}

// AC6: a containers_error heartbeat changes nothing, even for running
// servers absent from containers.
func TestE2EContainersErrorChangesNothing(t *testing.T) {
	e := newE2E(t, &instantAgent{})
	e.beat("a")
	srv := e.runningOn("a", "mc1")
	e.clk.Advance(time.Hour)
	before := e.state(srv.ID)
	hb := hbFor("a")
	hb.Containers = []shared.ContainerInfo{}
	hb.ContainersError = "exit status 1: Cannot connect to the Docker daemon"
	e.beatHB(hb)
	if after := e.state(srv.ID); after.State != shared.StateRunning || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("server changed: %+v -> %+v", before, after)
	}
	// The next good heartbeat still catches the missing container.
	e.beat("a")
	e.expect(srv.ID, shared.StateFailed, "container not running on agent")
}

// AC1/AC6 (wiring): orphans are logged, never killed, and a terminal server
// with a live container is not changed.
func TestE2EOrphansLoggedNotKilled(t *testing.T) {
	agent := &instantAgent{}
	e := newE2E(t, agent)
	e.beat("a")
	srv := e.runningOn("a", "mc1")
	if _, err := e.store.Transition(srv.ID, shared.StateFailed, "launch timed out", nil); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	var mu sync.Mutex
	prevLog := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) }))
	defer log.SetOutput(prevLog)

	e.clk.Advance(time.Hour)
	e.beat("a", running(srv.ID), running("ghost"))
	e.expect(srv.ID, shared.StateFailed, "launch timed out")
	if n := agent.stops.Load(); n != 0 {
		t.Errorf("agent Stop called %d times for orphans", n)
	}
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	for _, want := range []string{"c-ghost", "c-" + srv.ID} {
		if !strings.Contains(out, want) || !strings.Contains(out, "orphan") {
			t.Errorf("orphan %s not logged; log:\n%s", want, out)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// AC3 (wiring): runSweeper drives handleStatusChanges on its own ticker.
func TestRunSweeperMarksUnknown(t *testing.T) {
	e := newE2E(t, &instantAgent{})
	e.beat("a")
	srv := e.runningOn("a", "mc1")
	e.clk.Advance(defaultOfflineAfter + time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runSweeper(ctx, e.reg, e.store, time.Millisecond); close(done) }()
	waitState(t, e.h, srv.ID, shared.StateUnknown)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runSweeper did not return after cancel")
	}
}

// Stop on an unknown server returns 409 and does not call the agent.
func TestStopUnknownServerConflict(t *testing.T) {
	agent := &instantAgent{}
	e := newE2E(t, agent)
	e.beat("a")
	srv := e.runningOn("a", "mc1")
	e.clk.Advance(defaultOfflineAfter + time.Second)
	e.sweep()
	e.expect(srv.ID, shared.StateUnknown, "")
	assertError(t, doReq(e.h, http.MethodPost, "/servers/"+srv.ID+"/stop", ""), http.StatusConflict)
	e.expect(srv.ID, shared.StateUnknown, "")
	if n := agent.stops.Load(); n != 0 {
		t.Errorf("agent Stop called %d times", n)
	}
}

// AC6 through the HTTP heartbeat path: a server that moves to stopping
// between Reconcile's snapshot (store.List) and the apply is not touched.
// The store clock is read right after the snapshot, so a one-shot hook on it
// injects the concurrent stop at exactly that point.
func TestE2EStopBetweenSnapshotAndApply(t *testing.T) {
	clk := newFakeClock()
	var hook atomic.Pointer[func()]
	storeNow := func() time.Time {
		if f := hook.Swap(nil); f != nil {
			(*f)()
		}
		return clk.Now()
	}
	st := NewStore(storeNow)
	reg := NewRegistry(clk.Now, HealthThresholds{})
	h := withControllerKey(newControllerServer(st, reg, &instantAgent{}, testKey, testControllerKey))
	e := &e2e{t: t, clk: clk, store: st, reg: reg, h: h}
	e.beat("a")
	srv := e.runningOn("a", "mc1")
	clk.Advance(reconcileGrace + time.Second)

	fired := false
	f := func() {
		fired = true
		if _, err := st.Transition(srv.ID, shared.StateStopping, "", nil); err != nil {
			t.Errorf("inject stop: %v", err)
		}
	}
	hook.Store(&f)
	e.beat("a") // container missing: Reconcile wants running -> failed
	if !fired {
		t.Skip("store clock not read during heartbeat; cannot inject")
	}
	e.expect(srv.ID, shared.StateStopping, "")
}
