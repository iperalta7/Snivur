package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"snivur/v0/shared"
)

var allStates = []shared.ServerState{
	shared.StatePending, shared.StateStarting, shared.StateRunning,
	shared.StateStopping, shared.StateStopped, shared.StateFailed,
}

// pathTo lists the transitions that take a fresh (pending) server to state s.
// failed is reached from pending.
var pathTo = map[shared.ServerState][]shared.ServerState{
	shared.StatePending:  nil,
	shared.StateStarting: {shared.StateStarting},
	shared.StateRunning:  {shared.StateStarting, shared.StateRunning},
	shared.StateStopping: {shared.StateStarting, shared.StateRunning, shared.StateStopping},
	shared.StateStopped:  {shared.StateStarting, shared.StateRunning, shared.StateStopping, shared.StateStopped},
	shared.StateFailed:   {shared.StateFailed},
}

// fakeClock is a manually advanced clock, safe for concurrent use.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func createReq(name string) shared.CreateServerRequest {
	return shared.CreateServerRequest{Name: name, Game: "minecraft", Config: map[string]string{"image": "alpine"}}
}

// serverIn creates a server and drives it into state s.
func serverIn(t *testing.T, st *Store, name string, s shared.ServerState) shared.Server {
	t.Helper()
	srv, err := st.Create(createReq(name))
	if err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	for _, step := range pathTo[s] {
		if srv, err = st.Transition(srv.ID, step, "", nil); err != nil {
			t.Fatalf("setup %s -> %s: %v", srv.State, step, err)
		}
	}
	if srv.State != s {
		t.Fatalf("setup reached %s, want %s", srv.State, s)
	}
	return srv
}

// AC1: every (from, to) pair over all 6x6 states is either allowed or
// returns ErrInvalidTransition, exactly matching the ADR table.
func TestStoreTransitionTable(t *testing.T) {
	allowed := map[[2]shared.ServerState]bool{
		{shared.StatePending, shared.StateStarting}: true,
		{shared.StatePending, shared.StateFailed}:   true,
		{shared.StateStarting, shared.StateRunning}: true,
		{shared.StateStarting, shared.StateFailed}:  true,
		{shared.StateRunning, shared.StateStopping}: true,
		{shared.StateRunning, shared.StateFailed}:   true,
		{shared.StateStopping, shared.StateStopped}: true,
		{shared.StateStopping, shared.StateFailed}:  true,
	}
	n := 0
	for _, from := range allStates {
		for _, to := range allStates {
			n++
			want := allowed[[2]shared.ServerState{from, to}]
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				clk := newFakeClock()
				st := NewStore(clk.Now)
				srv := serverIn(t, st, "s", from)
				before, _ := st.Get(srv.ID)
				clk.Advance(time.Second)

				got, err := st.Transition(srv.ID, to, "msg", func(p *shared.Server) { p.ContainerID = "cid" })
				after, _ := st.Get(srv.ID)
				if want {
					if err != nil {
						t.Fatalf("allowed transition returned %v", err)
					}
					if got.State != to || after.State != to {
						t.Errorf("state = %s (stored %s), want %s", got.State, after.State, to)
					}
					if after.Message != "msg" || after.ContainerID != "cid" {
						t.Errorf("message/container = %q/%q, want msg/cid", after.Message, after.ContainerID)
					}
					if !after.UpdatedAt.Equal(clk.Now()) {
						t.Errorf("UpdatedAt = %v, want %v", after.UpdatedAt, clk.Now())
					}
					return
				}
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("err = %v, want ErrInvalidTransition", err)
				}
				// A rejected transition must change nothing.
				if after.State != before.State || after.Message != before.Message ||
					after.ContainerID != before.ContainerID || !after.UpdatedAt.Equal(before.UpdatedAt) {
					t.Errorf("rejected transition mutated server: before %+v after %+v", before, after)
				}
			})
		}
	}
	if n != 36 {
		t.Fatalf("checked %d pairs, want 36", n)
	}
}

// AC1: terminal states have no outgoing transitions.
func TestStoreTerminalStates(t *testing.T) {
	for _, term := range []shared.ServerState{shared.StateStopped, shared.StateFailed} {
		st := NewStore(nil)
		srv := serverIn(t, st, "s", term)
		for _, to := range allStates {
			if _, err := st.Transition(srv.ID, to, "", nil); !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("%s -> %s: err = %v, want ErrInvalidTransition", term, to, err)
			}
		}
	}
}

func TestStoreTransitionUnknownID(t *testing.T) {
	st := NewStore(nil)
	if _, err := st.Transition("nope", shared.StateStarting, "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStoreCreateAndGet(t *testing.T) {
	clk := newFakeClock()
	st := NewStore(clk.Now)
	srv, err := st.Create(createReq("mc1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.ID) != 32 {
		t.Errorf("ID %q, want 32 hex chars", srv.ID)
	}
	if srv.State != shared.StatePending {
		t.Errorf("state = %s, want pending", srv.State)
	}
	if !srv.CreatedAt.Equal(clk.Now()) || !srv.UpdatedAt.Equal(clk.Now()) {
		t.Errorf("timestamps = %v/%v, want %v", srv.CreatedAt, srv.UpdatedAt, clk.Now())
	}
	if srv.Name != "mc1" || srv.Game != "minecraft" || srv.Config["image"] != "alpine" {
		t.Errorf("fields not copied from request: %+v", srv)
	}
	got, ok := st.Get(srv.ID)
	if !ok || got.ID != srv.ID {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	if _, ok := st.Get("missing"); ok {
		t.Error("Get(missing) ok = true")
	}
}

// AC5 (store level): a name held by a non-terminal server conflicts; once
// the holder is terminal the name can be reused.
func TestStoreNameConflict(t *testing.T) {
	for _, holder := range allStates {
		t.Run(string(holder), func(t *testing.T) {
			st := NewStore(nil)
			serverIn(t, st, "dup", holder)
			_, err := st.Create(createReq("dup"))
			terminal := holder == shared.StateStopped || holder == shared.StateFailed
			if terminal && err != nil {
				t.Fatalf("reuse after %s: %v", holder, err)
			}
			if !terminal && !errors.Is(err, ErrNameConflict) {
				t.Fatalf("err = %v, want ErrNameConflict", err)
			}
		})
	}
}

func TestStoreListSortedByCreatedAt(t *testing.T) {
	clk := newFakeClock()
	st := NewStore(clk.Now)
	if l := st.List(); l == nil || len(l) != 0 {
		t.Fatalf("empty List = %#v, want non-nil empty", l)
	}
	var want []string
	for _, n := range []string{"a", "b", "c", "d"} {
		srv, err := st.Create(createReq(n))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, srv.ID)
		clk.Advance(time.Millisecond)
	}
	// Transitioning the first one must not reorder (UpdatedAt is not the key).
	if _, err := st.Transition(want[0], shared.StateStarting, "", nil); err != nil {
		t.Fatal(err)
	}
	got := st.List()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Errorf("List[%d] = %s, want %s", i, got[i].ID, want[i])
		}
	}
}

// Every method returns copies: mutating results must not affect the store.
func TestStoreReturnsCopies(t *testing.T) {
	st := NewStore(nil)
	req := createReq("mc1")
	srv, err := st.Create(req)
	if err != nil {
		t.Fatal(err)
	}

	// Caller's request map must not be aliased either.
	req.Config["image"] = "req-mutated"
	// Create's result.
	srv.Config["image"] = "create-mutated"
	srv.Name = "create-mutated"
	srv.State = shared.StateRunning

	g, _ := st.Get(srv.ID)
	g.Config["image"] = "get-mutated"
	g.Config["new"] = "x"
	g.ContainerID = "get-mutated"

	l := st.List()
	l[0].Config["image"] = "list-mutated"
	l[0].Message = "list-mutated"

	tr, err := st.Transition(srv.ID, shared.StateStarting, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Config["image"] != "alpine" {
		t.Fatalf("store config already corrupted before transition result: %v", tr.Config)
	}
	tr.Config["image"] = "transition-mutated"

	// mutate must not retain a usable pointer into the store.
	var leaked *shared.Server
	var leakedCfg map[string]string
	if _, err := st.Transition(srv.ID, shared.StateRunning, "", func(p *shared.Server) {
		leaked = p
		leakedCfg = p.Config
	}); err != nil {
		t.Fatal(err)
	}
	leaked.Name = "leaked"
	leaked.ContainerID = "leaked"
	leakedCfg["image"] = "leaked"

	final, _ := st.Get(srv.ID)
	if final.Name != "mc1" || final.State != shared.StateRunning || final.ContainerID != "" || final.Message != "" {
		t.Errorf("store mutated through returned copy: %+v", final)
	}
	if len(final.Config) != 1 || final.Config["image"] != "alpine" {
		t.Errorf("store config mutated through returned copy: %v", final.Config)
	}
}

func TestStoreTransitionMutateCannotOverrideOwnedFields(t *testing.T) {
	clk := newFakeClock()
	st := NewStore(clk.Now)
	srv, _ := st.Create(createReq("mc1"))
	clk.Advance(time.Minute)
	got, err := st.Transition(srv.ID, shared.StateStarting, "m", func(p *shared.Server) {
		p.ID = "hijack"
		p.State = shared.StateStopped
		p.Message = "x"
		p.CreatedAt = time.Time{}
		p.UpdatedAt = time.Time{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != srv.ID || got.State != shared.StateStarting || got.Message != "m" ||
		!got.CreatedAt.Equal(srv.CreatedAt) || !got.UpdatedAt.Equal(clk.Now()) {
		t.Errorf("mutate overrode owned fields: %+v", got)
	}
	if _, ok := st.Get("hijack"); ok {
		t.Error("server reachable under hijacked ID")
	}
}
