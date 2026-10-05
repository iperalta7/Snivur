package main

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"snivur/v0/shared"
)

func hbFor(id string, containers ...shared.ContainerInfo) shared.Heartbeat {
	return shared.Heartbeat{
		AgentID:    id,
		Hostname:   "host-" + id,
		Address:    "http://" + id + ":8000",
		Version:    "v1",
		Containers: containers,
		SentAt:     time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), // must not be used as LastSeen
	}
}

func mustGet(t *testing.T, r *Registry, id string) shared.Agent {
	t.Helper()
	a, ok := r.Get(id)
	if !ok {
		t.Fatalf("agent %q not in registry", id)
	}
	return a
}

// AC2: health boundaries with a fake clock, for the explicit defaults and
// for zero thresholds (which must default to 30s/90s).
func TestRegistryHealthBoundaries(t *testing.T) {
	for name, th := range map[string]HealthThresholds{
		"explicit 30s/90s": {Unhealthy: 30 * time.Second, Offline: 90 * time.Second},
		"zero -> defaults": {},
	} {
		t.Run(name, func(t *testing.T) {
			clk := newFakeClock()
			r := NewRegistry(clk.Now, th)
			t0 := clk.Now()
			a := r.Upsert(hbFor("a"))
			if a.Status != shared.AgentHealthy || !a.LastSeen.Equal(t0) {
				t.Fatalf("Upsert = %+v, want healthy with LastSeen=%v (controller clock, not SentAt)", a, t0)
			}

			steps := []struct {
				at   time.Duration
				want shared.AgentStatus
			}{
				{0, shared.AgentHealthy},
				{30 * time.Second, shared.AgentHealthy},
				{31 * time.Second, shared.AgentUnhealthy},
				{90 * time.Second, shared.AgentUnhealthy},
				{91 * time.Second, shared.AgentOffline},
			}
			for _, s := range steps {
				clk.Advance(t0.Add(s.at).Sub(clk.Now()))
				r.Sweep()
				if got := mustGet(t, r, "a").Status; got != s.want {
					t.Errorf("+%v: status = %s, want %s", s.at, got, s.want)
				}
			}

			// A new heartbeat brings it straight back to healthy.
			a = r.Upsert(hbFor("a"))
			if a.Status != shared.AgentHealthy || !a.LastSeen.Equal(clk.Now()) {
				t.Errorf("re-Upsert = %+v, want healthy, LastSeen=%v", a, clk.Now())
			}
			if got := mustGet(t, r, "a").Status; got != shared.AgentHealthy {
				t.Errorf("after new heartbeat: status = %s, want healthy", got)
			}
			if ch := r.Sweep(); len(ch) != 0 {
				t.Errorf("sweep right after heartbeat = %+v, want none", ch)
			}
		})
	}
}

// Non-default thresholds are honoured.
func TestRegistryCustomThresholds(t *testing.T) {
	clk := newFakeClock()
	r := NewRegistry(clk.Now, HealthThresholds{Unhealthy: time.Second, Offline: 2 * time.Second})
	r.Upsert(hbFor("a"))
	clk.Advance(1500 * time.Millisecond)
	r.Sweep()
	if got := mustGet(t, r, "a").Status; got != shared.AgentUnhealthy {
		t.Errorf("+1.5s: %s, want unhealthy", got)
	}
	clk.Advance(time.Second)
	r.Sweep()
	if got := mustGet(t, r, "a").Status; got != shared.AgentOffline {
		t.Errorf("+2.5s: %s, want offline", got)
	}
}

// AC3: Sweep returns only agents whose status changed; a second sweep at the
// same time returns nothing.
func TestRegistrySweepReturnsOnlyChanges(t *testing.T) {
	clk := newFakeClock()
	r := NewRegistry(clk.Now, HealthThresholds{})
	if ch := r.Sweep(); len(ch) != 0 {
		t.Fatalf("empty registry sweep = %+v", ch)
	}
	r.Upsert(hbFor("c"))
	r.Upsert(hbFor("a"))
	clk.Advance(20 * time.Second)
	r.Upsert(hbFor("b")) // b is 20s fresher than a and c

	if ch := r.Sweep(); len(ch) != 0 {
		t.Errorf("sweep with all healthy = %+v, want none", ch)
	}

	clk.Advance(11 * time.Second) // a,c at +31s; b at +11s
	want := []StatusChange{
		{AgentID: "a", From: shared.AgentHealthy, To: shared.AgentUnhealthy},
		{AgentID: "c", From: shared.AgentHealthy, To: shared.AgentUnhealthy},
	}
	if got := r.Sweep(); !reflect.DeepEqual(got, want) {
		t.Errorf("sweep at +31s = %+v, want %+v", got, want)
	}
	if got := r.Sweep(); len(got) != 0 {
		t.Errorf("second sweep at same time = %+v, want none", got)
	}

	clk.Advance(60 * time.Second) // a,c at +91s; b at +71s
	want = []StatusChange{
		{AgentID: "a", From: shared.AgentUnhealthy, To: shared.AgentOffline},
		{AgentID: "b", From: shared.AgentHealthy, To: shared.AgentUnhealthy},
		{AgentID: "c", From: shared.AgentUnhealthy, To: shared.AgentOffline},
	}
	if got := r.Sweep(); !reflect.DeepEqual(got, want) {
		t.Errorf("sweep at +91s = %+v, want %+v", got, want)
	}
	if got := r.Sweep(); len(got) != 0 {
		t.Errorf("second sweep at same time = %+v, want none", got)
	}

	// A sweep that skips the unhealthy window reports healthy -> offline.
	r.Upsert(hbFor("a"))
	clk.Advance(5 * time.Minute)
	got := r.Sweep()
	found := false
	for _, c := range got {
		if c.AgentID == "a" {
			found = true
			if c.From != shared.AgentHealthy || c.To != shared.AgentOffline {
				t.Errorf("a: %+v, want healthy -> offline", c)
			}
		}
	}
	if !found {
		t.Errorf("sweep after 5m = %+v, missing a", got)
	}
}

// Upsert replaces the agent's fields; List is sorted by ID.
func TestRegistryUpsertAndList(t *testing.T) {
	clk := newFakeClock()
	r := NewRegistry(clk.Now, HealthThresholds{})
	if l := r.List(); l == nil || len(l) != 0 {
		t.Errorf("empty List = %#v, want empty non-nil", l)
	}
	for _, id := range []string{"zeta", "alpha", "mid"} {
		r.Upsert(hbFor(id))
	}
	hb := hbFor("alpha", shared.ContainerInfo{ServerID: "s1", ContainerID: "c1", State: "running"})
	hb.Address = "http://new:9000"
	hb.Version = "v2"
	clk.Advance(time.Second)
	r.Upsert(hb)

	l := r.List()
	var ids []string
	for _, a := range l {
		ids = append(ids, a.ID)
	}
	if !reflect.DeepEqual(ids, []string{"alpha", "mid", "zeta"}) {
		t.Errorf("List IDs = %v, want sorted [alpha mid zeta]", ids)
	}
	a := l[0]
	want := shared.Agent{
		ID: "alpha", Hostname: "host-alpha", Address: "http://new:9000", Version: "v2",
		Status: shared.AgentHealthy, LastSeen: clk.Now(),
		Containers: []shared.ContainerInfo{{ServerID: "s1", ContainerID: "c1", State: "running"}},
	}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("alpha = %+v\nwant    %+v", a, want)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(unknown) ok = true")
	}
}

// Copy semantics: neither the caller's input nor any returned value aliases
// the registry's Containers slice.
func TestRegistryReturnsCopies(t *testing.T) {
	r := NewRegistry(newFakeClock().Now, HealthThresholds{})
	orig := shared.ContainerInfo{ServerID: "s1", ContainerID: "c1", State: "running"}
	in := []shared.ContainerInfo{orig}
	up := r.Upsert(hbFor("a", in...))
	in[0].State = "MUTATED-input"
	up.Containers[0].State = "MUTATED-upsert"

	g, _ := r.Get("a")
	if g.Containers[0] != orig {
		t.Fatalf("registry aliased input or Upsert result: %+v", g.Containers[0])
	}
	g.Containers[0].State = "MUTATED-get"
	g.Containers = append(g.Containers, orig)
	l := r.List()
	if len(l[0].Containers) != 1 || l[0].Containers[0] != orig {
		t.Fatalf("Get result aliased: %+v", l[0].Containers)
	}
	l[0].Containers[0].State = "MUTATED-list"
	l[0].Status = shared.AgentOffline
	if g2 := mustGet(t, r, "a"); g2.Containers[0] != orig || g2.Status != shared.AgentHealthy {
		t.Fatalf("List result aliased: %+v", g2)
	}

	// Nil containers come back as an empty slice (encodes as []).
	r.Upsert(hbFor("b"))
	if b := mustGet(t, r, "b"); b.Containers == nil {
		t.Error("nil Containers returned; want empty slice so JSON is []")
	}
}

// runSweeper actually sweeps on its interval and stops on ctx cancel.
func TestRunSweeperSweepsAndStops(t *testing.T) {
	clk := newFakeClock()
	r := NewRegistry(clk.Now, HealthThresholds{})
	r.Upsert(hbFor("a"))
	clk.Advance(91 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSweeper(ctx, r, time.Millisecond)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for mustGet(t, r, "a").Status != shared.AgentOffline {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("runSweeper never marked the agent offline")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runSweeper did not return after cancel")
	}
}

// The production sweep interval is 5s per the spec.
func TestSweepIntervalIs5s(t *testing.T) {
	if sweepInterval != 5*time.Second {
		t.Errorf("sweepInterval = %v, want 5s", sweepInterval)
	}
	if d := DefaultHealthThresholds(); d.Unhealthy != 30*time.Second || d.Offline != 90*time.Second {
		t.Errorf("DefaultHealthThresholds = %+v, want 30s/90s", d)
	}
}

// AC7: concurrent Upsert, Sweep, List and Get (plus a moving clock) under
// -race, with 64 goroutines. Also checks List stays sorted and every
// returned agent is internally consistent.
func TestRegistryConcurrent(t *testing.T) {
	clk := newFakeClock()
	r := NewRegistry(clk.Now, HealthThresholds{})
	const (
		goroutines = 64
		iters      = 300
		agents     = 8
	)
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("agent-%d", (g+i)%agents)
				switch g % 5 {
				case 0, 1:
					a := r.Upsert(hbFor(id, shared.ContainerInfo{ServerID: id, ContainerID: fmt.Sprint(i), State: "running"}))
					a.Containers[0].State = "x" // must not race with readers
				case 2:
					for _, c := range r.Sweep() {
						if c.From == c.To {
							errs <- fmt.Sprintf("no-op change reported: %+v", c)
							return
						}
					}
				case 3:
					l := r.List()
					for j := 1; j < len(l); j++ {
						if l[j-1].ID >= l[j].ID {
							errs <- fmt.Sprintf("List not sorted: %s, %s", l[j-1].ID, l[j].ID)
							return
						}
					}
					for _, a := range l {
						if len(a.Containers) > 0 {
							a.Containers[0].State = "y"
						}
					}
				case 4:
					if a, ok := r.Get(id); ok {
						if len(a.Containers) != 1 || a.Containers[0].ServerID != id {
							errs <- fmt.Sprintf("inconsistent agent %+v", a)
							return
						}
					}
					clk.Advance(time.Second)
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if n := len(r.List()); n != agents {
		t.Errorf("List has %d agents, want %d", n, agents)
	}
	for _, a := range r.List() {
		if a.Containers[0].State != "running" {
			t.Errorf("returned copy mutation leaked into registry: %+v", a)
		}
	}
}

// AC5: Upsert copies ContainersError, and a clean heartbeat clears it.
func TestRegistryContainersError(t *testing.T) {
	r := NewRegistry(newFakeClock().Now, HealthThresholds{})
	hb := hbFor("a")
	hb.ContainersError = "boom"
	if a := r.Upsert(hb); a.ContainersError != "boom" {
		t.Errorf("Upsert ContainersError = %q", a.ContainersError)
	}
	if a := mustGet(t, r, "a"); a.ContainersError != "boom" {
		t.Errorf("Get ContainersError = %q", a.ContainersError)
	}
	r.Upsert(hbFor("a"))
	if a := mustGet(t, r, "a"); a.ContainersError != "" {
		t.Errorf("ContainersError = %q after clean heartbeat, want empty", a.ContainersError)
	}
}
