package main

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"snivur/v0/shared"
)

// ADR 0004 tests for the pure Reconcile function, TransitionFrom and the
// state machine extended with unknown.

var recNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// old is comfortably outside reconcileGrace.
var old = recNow.Add(-time.Hour)

func srvAt(id, agent string, s shared.ServerState, updated time.Time) shared.Server {
	return shared.Server{ID: id, AgentID: agent, Name: id, State: s, CreatedAt: updated, UpdatedAt: updated}
}

func ci(serverID, state string) shared.ContainerInfo {
	return shared.ContainerInfo{ServerID: serverID, ContainerID: "c-" + serverID + "-" + state, State: state}
}

func hbA(containers ...shared.ContainerInfo) shared.Heartbeat {
	return shared.Heartbeat{AgentID: "a", Address: "http://a:8000", Containers: containers}
}

// AC1: every row of the ADR Reconcile table, for container "running",
// "exited" (counts as not running) and absent.
func TestReconcileTable(t *testing.T) {
	if reconcileGrace != 15*time.Second {
		t.Errorf("reconcileGrace = %v, want 15s", reconcileGrace)
	}
	type want struct {
		to  shared.ServerState // "" = no transition
		msg string
	}
	notRunningFailed := want{shared.StateFailed, "container not running on agent"}
	unknownFailed := want{shared.StateFailed, "container not found after agent recovered"}
	cases := []struct {
		state     shared.ServerState
		container string // "running", "exited", "" (absent)
		want      want
	}{
		{shared.StateRunning, "running", want{}},
		{shared.StateRunning, "", notRunningFailed},
		{shared.StateRunning, "exited", notRunningFailed},
		{shared.StateUnknown, "running", want{shared.StateRunning, "agent recovered"}},
		{shared.StateUnknown, "", unknownFailed},
		{shared.StateUnknown, "exited", unknownFailed},
	}
	for _, s := range []shared.ServerState{shared.StatePending, shared.StateStarting, shared.StateStopping, shared.StateStopped, shared.StateFailed} {
		for _, c := range []string{"running", "exited", ""} {
			cases = append(cases, struct {
				state     shared.ServerState
				container string
				want      want
			}{s, c, want{}})
		}
	}
	for _, tc := range cases {
		name := fmt.Sprintf("%s/container=%q", tc.state, tc.container)
		t.Run(name, func(t *testing.T) {
			servers := []shared.Server{srvAt("s1", "a", tc.state, old)}
			var cs []shared.ContainerInfo
			if tc.container != "" {
				cs = append(cs, ci("s1", tc.container))
			}
			tr, _ := Reconcile(servers, hbA(cs...), recNow)
			if tc.want.to == "" {
				if len(tr) != 0 {
					t.Fatalf("transitions = %+v, want none", tr)
				}
				return
			}
			wantT := []Transition{{ServerID: "s1", From: tc.state, To: tc.want.to, Message: tc.want.msg}}
			if !reflect.DeepEqual(tr, wantT) {
				t.Fatalf("transitions = %+v, want %+v", tr, wantT)
			}
		})
	}
}

// AC1: servers bound to another agent are ignored.
func TestReconcileIgnoresOtherAgents(t *testing.T) {
	servers := []shared.Server{
		srvAt("b-run", "b", shared.StateRunning, old),
		srvAt("b-unk", "b", shared.StateUnknown, old),
		srvAt("none", "", shared.StateRunning, old),
	}
	tr, orph := Reconcile(servers, hbA(), recNow)
	if len(tr) != 0 || len(orph) != 0 {
		t.Fatalf("got transitions %+v orphans %+v, want none", tr, orph)
	}
	// And a heartbeat from b only touches b's servers.
	servers = append(servers, srvAt("a-run", "a", shared.StateRunning, old))
	tr, _ = Reconcile(servers, shared.Heartbeat{AgentID: "b"}, recNow)
	ids := []string{}
	for _, x := range tr {
		ids = append(ids, x.ServerID)
	}
	if !reflect.DeepEqual(ids, []string{"b-run", "b-unk"}) {
		t.Errorf("transition IDs = %v, want [b-run b-unk]", ids)
	}
}

// AC1: orphan detection.
func TestReconcileOrphans(t *testing.T) {
	servers := []shared.Server{
		srvAt("stopped", "a", shared.StateStopped, old),
		srvAt("failed", "a", shared.StateFailed, old),
		srvAt("stopped-exited", "a", shared.StateStopped, old),
		srvAt("failed-exited", "a", shared.StateFailed, old),
		srvAt("running", "a", shared.StateRunning, old),
		srvAt("starting", "a", shared.StateStarting, old),
		srvAt("stopping", "a", shared.StateStopping, old),
		srvAt("unknown", "a", shared.StateUnknown, old),
	}
	cs := []shared.ContainerInfo{
		ci("ghost", "running"),       // orphan: no such server
		ci("ghost-exited", "exited"), // not an orphan: not running
		ci("stopped", "running"),     // orphan: terminal server
		ci("failed", "running"),      // orphan: terminal server
		ci("stopped-exited", "exited"),
		ci("failed-exited", "exited"),
		ci("running", "running"),
		ci("starting", "running"),
		ci("stopping", "running"),
		ci("unknown", "running"),
	}
	_, orph := Reconcile(servers, hbA(cs...), recNow)
	want := []shared.ContainerInfo{ci("failed", "running"), ci("ghost", "running"), ci("stopped", "running")}
	if !reflect.DeepEqual(orph, want) {
		t.Fatalf("orphans = %+v\nwant %+v", orph, want)
	}
}

// AC1: output is deterministic, sorted by ServerID, for shuffled inputs.
func TestReconcileDeterministicOrder(t *testing.T) {
	var servers []shared.Server
	var cs []shared.ContainerInfo
	var wantIDs []string
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("s%02d", i)
		wantIDs = append(wantIDs, id)
		if i%2 == 0 {
			servers = append(servers, srvAt(id, "a", shared.StateRunning, old)) // -> failed
		} else {
			servers = append(servers, srvAt(id, "a", shared.StateUnknown, old)) // -> running
			cs = append(cs, ci(id, "running"))
		}
		cs = append(cs, ci(fmt.Sprintf("ghost%02d", i), "running"))
	}
	rng := rand.New(rand.NewSource(1))
	var first []Transition
	var firstO []shared.ContainerInfo
	for iter := 0; iter < 50; iter++ {
		rng.Shuffle(len(servers), func(i, j int) { servers[i], servers[j] = servers[j], servers[i] })
		rng.Shuffle(len(cs), func(i, j int) { cs[i], cs[j] = cs[j], cs[i] })
		tr, orph := Reconcile(append([]shared.Server(nil), servers...), hbA(append([]shared.ContainerInfo(nil), cs...)...), recNow)
		var ids []string
		for _, x := range tr {
			ids = append(ids, x.ServerID)
		}
		if !reflect.DeepEqual(ids, wantIDs) {
			t.Fatalf("iter %d: transition order %v, want %v", iter, ids, wantIDs)
		}
		if iter == 0 {
			first, firstO = tr, orph
			continue
		}
		if !reflect.DeepEqual(tr, first) || !reflect.DeepEqual(orph, firstO) {
			t.Fatalf("iter %d: output differs from first run", iter)
		}
	}
	for i := 1; i < len(firstO); i++ {
		if firstO[i].ServerID < firstO[i-1].ServerID {
			t.Errorf("orphans not sorted: %+v", firstO)
		}
	}
}

// AC1/AC6: the grace boundary. Exactly reconcileGrace is still within the
// grace period (skipped); just over is failed.
func TestReconcileGraceBoundary(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
		fail bool
	}{
		{"just updated", 0, false},
		{"1s", time.Second, false},
		{"14.999s", reconcileGrace - time.Millisecond, false},
		{"exactly 15s", reconcileGrace, false},
		{"15s+1ns", reconcileGrace + time.Nanosecond, true},
		{"16s", reconcileGrace + time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			servers := []shared.Server{srvAt("s1", "a", shared.StateRunning, recNow.Add(-tc.age))}
			tr, _ := Reconcile(servers, hbA(), recNow)
			if got := len(tr) == 1 && tr[0].To == shared.StateFailed; got != tc.fail {
				t.Errorf("age %v: transitions %+v, want failed=%v", tc.age, tr, tc.fail)
			}
			// A running container is always a no-op, grace or not.
			if tr, _ := Reconcile(servers, hbA(ci("s1", "running")), recNow); len(tr) != 0 {
				t.Errorf("container running: transitions %+v", tr)
			}
		})
	}
}

// AC1/AC6: containers_error returns nothing at all.
func TestReconcileContainersError(t *testing.T) {
	servers := []shared.Server{
		srvAt("run", "a", shared.StateRunning, old),
		srvAt("unk", "a", shared.StateUnknown, old),
		srvAt("failed", "a", shared.StateFailed, old),
	}
	hb := hbA(ci("ghost", "running"), ci("failed", "running"), ci("unk", "running"))
	hb.ContainersError = "docker ps: Cannot connect to the Docker daemon"
	tr, orph := Reconcile(servers, hb, recNow)
	if tr != nil || orph != nil {
		t.Fatalf("got transitions %+v orphans %+v, want nil, nil", tr, orph)
	}
}

// Reconcile is pure: it does not modify its inputs.
func TestReconcileDoesNotMutateInput(t *testing.T) {
	servers := []shared.Server{srvAt("b", "a", shared.StateRunning, old), srvAt("a", "a", shared.StateUnknown, old)}
	cs := []shared.ContainerInfo{ci("z", "running"), ci("a", "running")}
	sCopy := append([]shared.Server(nil), servers...)
	cCopy := append([]shared.ContainerInfo(nil), cs...)
	Reconcile(servers, hbA(cs...), recNow)
	if !reflect.DeepEqual(servers, sCopy) || !reflect.DeepEqual(cs, cCopy) {
		t.Error("Reconcile mutated its input")
	}
}

// --- state machine with unknown -------------------------------------------

var allStates7 = append(append([]shared.ServerState(nil), allStates...), shared.StateUnknown)

var pathTo7 = func() map[shared.ServerState][]shared.ServerState {
	m := map[shared.ServerState][]shared.ServerState{}
	for k, v := range pathTo {
		m[k] = v
	}
	m[shared.StateUnknown] = []shared.ServerState{shared.StateStarting, shared.StateRunning, shared.StateUnknown}
	return m
}()

func serverIn7(t *testing.T, st *Store, name, agentID string, s shared.ServerState) shared.Server {
	t.Helper()
	req := createReq(name)
	req.AgentID = agentID
	srv, err := st.Create(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range pathTo7[s] {
		if srv, err = st.Transition(srv.ID, step, "", nil); err != nil {
			t.Fatalf("setup -> %s: %v", step, err)
		}
	}
	if srv.State != s {
		t.Fatalf("setup reached %s, want %s", srv.State, s)
	}
	return srv
}

// State machine over all 7x7 pairs, including unknown.
func TestStoreTransitionTableWithUnknown(t *testing.T) {
	allowed := map[[2]shared.ServerState]bool{
		{shared.StatePending, shared.StateStarting}: true,
		{shared.StatePending, shared.StateFailed}:   true,
		{shared.StateStarting, shared.StateRunning}: true,
		{shared.StateStarting, shared.StateFailed}:  true,
		{shared.StateRunning, shared.StateStopping}: true,
		{shared.StateRunning, shared.StateFailed}:   true,
		{shared.StateRunning, shared.StateUnknown}:  true,
		{shared.StateStopping, shared.StateStopped}: true,
		{shared.StateStopping, shared.StateFailed}:  true,
		{shared.StateUnknown, shared.StateRunning}:  true,
		{shared.StateUnknown, shared.StateFailed}:   true,
	}
	n := 0
	for _, from := range allStates7 {
		for _, to := range allStates7 {
			n++
			want := allowed[[2]shared.ServerState{from, to}]
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				clk := newFakeClock()
				st := NewStore(clk.Now)
				srv := serverIn7(t, st, "s", "a", from)
				before, _ := st.Get(srv.ID)
				clk.Advance(time.Second)
				_, err := st.Transition(srv.ID, to, "msg", nil)
				after, _ := st.Get(srv.ID)
				if want {
					if err != nil || after.State != to || !after.UpdatedAt.Equal(clk.Now()) {
						t.Fatalf("allowed: err %v state %s", err, after.State)
					}
					return
				}
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("err = %v, want ErrInvalidTransition", err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Errorf("rejected transition mutated: %+v -> %+v", before, after)
				}
			})
		}
	}
	if n != 49 {
		t.Fatalf("checked %d pairs, want 49", n)
	}
}

// AC6: TransitionFrom is a compare-and-set.
func TestStoreTransitionFrom(t *testing.T) {
	t.Run("mismatch returns ErrStateChanged and changes nothing", func(t *testing.T) {
		for _, cur := range allStates7 {
			for _, from := range allStates7 {
				if from == cur {
					continue
				}
				clk := newFakeClock()
				st := NewStore(clk.Now)
				srv := serverIn7(t, st, "s", "a", cur)
				before, _ := st.Get(srv.ID)
				clk.Advance(time.Second)
				called := false
				for _, to := range allStates7 {
					_, err := st.TransitionFrom(srv.ID, from, to, "msg", func(p *shared.Server) { called = true; p.ContainerID = "x" })
					if !errors.Is(err, ErrStateChanged) {
						t.Errorf("cur %s from %s to %s: err = %v, want ErrStateChanged", cur, from, to, err)
					}
				}
				after, _ := st.Get(srv.ID)
				if called || !reflect.DeepEqual(before, after) {
					t.Errorf("cur %s from %s: mutated (called=%v) %+v -> %+v", cur, from, called, before, after)
				}
			}
		}
	})
	t.Run("match applies", func(t *testing.T) {
		clk := newFakeClock()
		st := NewStore(clk.Now)
		srv := serverIn7(t, st, "s", "a", shared.StateRunning)
		clk.Advance(time.Second)
		got, err := st.TransitionFrom(srv.ID, shared.StateRunning, shared.StateUnknown, "agent offline", nil)
		if err != nil || got.State != shared.StateUnknown || got.Message != "agent offline" || !got.UpdatedAt.Equal(clk.Now()) {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
	t.Run("match but forbidden returns ErrInvalidTransition", func(t *testing.T) {
		st := NewStore(nil)
		srv := serverIn7(t, st, "s", "a", shared.StateUnknown)
		_, err := st.TransitionFrom(srv.ID, shared.StateUnknown, shared.StateStopping, "", nil)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("err = %v, want ErrInvalidTransition", err)
		}
		if got, _ := st.Get(srv.ID); got.State != shared.StateUnknown {
			t.Errorf("state = %s", got.State)
		}
	})
	t.Run("unknown id", func(t *testing.T) {
		_, err := NewStore(nil).TransitionFrom("nope", shared.StateRunning, shared.StateFailed, "", nil)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// AC6: a server moved to stopping between the Reconcile snapshot and the
// apply is not touched.
func TestReconcileStaleSnapshotNotApplied(t *testing.T) {
	clk := newFakeClock()
	st := NewStore(clk.Now)
	srv := serverIn7(t, st, "s", "a", shared.StateRunning)
	clk.Advance(reconcileGrace + time.Second)

	tr, _ := Reconcile(st.List(), hbA(), clk.Now())
	if len(tr) != 1 || tr[0].From != shared.StateRunning || tr[0].To != shared.StateFailed {
		t.Fatalf("transitions = %+v, want running -> failed", tr)
	}
	// Concurrent stop wins the race.
	if _, err := st.Transition(srv.ID, shared.StateStopping, "", nil); err != nil {
		t.Fatal(err)
	}
	before, _ := st.Get(srv.ID)
	clk.Advance(time.Second)
	for _, x := range tr {
		if _, err := st.TransitionFrom(x.ServerID, x.From, x.To, x.Message, nil); !errors.Is(err, ErrStateChanged) {
			t.Errorf("apply err = %v, want ErrStateChanged", err)
		}
	}
	after, _ := st.Get(srv.ID)
	if !reflect.DeepEqual(before, after) || after.State != shared.StateStopping {
		t.Errorf("server touched: %+v -> %+v", before, after)
	}
}
