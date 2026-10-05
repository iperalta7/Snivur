package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"snivur/v0/shared"
)

// launchedIDs returns the server IDs of /launch hits and the IDs in
// /servers/{id}/stop hits, checking the API key on each.
func agentHitsByKind(t *testing.T, fa *fakeAgent) (launches, stops []string) {
	t.Helper()
	for _, h := range fa.Hits() {
		if h.key != testKey {
			t.Errorf("agent hit %s %s with key %q", h.method, h.path, h.key)
		}
		switch {
		case h.method == http.MethodPost && h.path == "/launch":
			launches = append(launches, h.req.ServerID)
		case h.method == http.MethodPost && strings.HasPrefix(h.path, "/servers/") && strings.HasSuffix(h.path, "/stop"):
			stops = append(stops, strings.TrimSuffix(strings.TrimPrefix(h.path, "/servers/"), "/stop"))
		default:
			t.Errorf("unexpected agent hit %s %s", h.method, h.path)
		}
	}
	return
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func listIDs(t *testing.T, h http.Handler, query string) []string {
	t.Helper()
	w := doReq(h, http.MethodGet, "/servers"+query, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /servers%s: %d", query, w.Code)
	}
	var list []shared.Server
	singleJSON(t, w.Body.Bytes(), &list)
	ids := []string{}
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	return ids
}

// AC4: two httptest agents; launch and stop for each server hit only the
// agent it is bound to. Also the ?agent_id= filter.
func TestRoutingTwoAgents(t *testing.T) {
	A := newFakeAgent(t, http.StatusOK, `{"container_id":"cid"}`)
	B := newFakeAgent(t, http.StatusOK, `{"container_id":"cid"}`)
	st := NewStore(time.Now)
	reg := NewRegistry(time.Now, HealthThresholds{})
	h := withControllerKey(newControllerServer(st, reg, newHTTPAgentClient(testKey, nil), testKey, testControllerKey))
	for id, fa := range map[string]*fakeAgent{"A": A, "B": B} {
		hb := hbFor(id)
		hb.Address = fa.URL
		if w := postHeartbeat(h, strp(testKey), hbJSON(t, hb)); w.Code != http.StatusNoContent {
			t.Fatalf("heartbeat %s: %d", id, w.Code)
		}
	}

	var onA, onB []string
	for i := 0; i < 3; i++ {
		for _, agent := range []string{"A", "B"} {
			body := fmt.Sprintf(`{"agent_id":%q,"name":"%s-%d","game":"g","config":{"image":"alpine"}}`, agent, agent, i)
			w := doReq(h, http.MethodPost, "/servers", body)
			if w.Code != http.StatusAccepted {
				t.Fatalf("create: %d %s", w.Code, w.Body)
			}
			var srv shared.Server
			singleJSON(t, w.Body.Bytes(), &srv)
			if srv.AgentID != agent {
				t.Errorf("agent_id = %q, want %q", srv.AgentID, agent)
			}
			waitState(t, h, srv.ID, shared.StateRunning)
			if agent == "A" {
				onA = append(onA, srv.ID)
			} else {
				onB = append(onB, srv.ID)
			}
		}
	}

	la, sa := agentHitsByKind(t, A)
	lb, sb := agentHitsByKind(t, B)
	if !sameSet(la, onA) || !sameSet(lb, onB) || len(sa)+len(sb) != 0 {
		t.Fatalf("launch routing: A got %v (want %v), B got %v (want %v); stops %v %v", la, onA, lb, onB, sa, sb)
	}

	// ?agent_id= filter.
	if got := listIDs(t, h, "?agent_id=A"); !sameSet(got, onA) {
		t.Errorf("?agent_id=A = %v, want %v", got, onA)
	}
	if got := listIDs(t, h, "?agent_id=B"); !sameSet(got, onB) {
		t.Errorf("?agent_id=B = %v, want %v", got, onB)
	}
	if got := listIDs(t, h, "?agent_id=nope"); len(got) != 0 {
		t.Errorf("?agent_id=nope = %v, want []", got)
	}
	if w := doReq(h, http.MethodGet, "/servers?agent_id=nope", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("empty filter body = %q, want []", w.Body)
	}
	if got := listIDs(t, h, ""); !sameSet(got, append(append([]string{}, onA...), onB...)) {
		t.Errorf("unfiltered = %v", got)
	}

	// Stops route the same way.
	for _, id := range append(append([]string{}, onA...), onB...) {
		if w := doReq(h, http.MethodPost, "/servers/"+id+"/stop", ""); w.Code != http.StatusAccepted {
			t.Fatalf("stop %s: %d %s", id, w.Code, w.Body)
		}
		waitState(t, h, id, shared.StateStopped)
	}
	_, sa = agentHitsByKind(t, A)
	_, sb = agentHitsByKind(t, B)
	if !sameSet(sa, onA) || !sameSet(sb, onB) {
		t.Fatalf("stop routing: A got %v (want %v), B got %v (want %v)", sa, onA, sb, onB)
	}
}

// AC4: Stop resolves the agent's address at stop time (a re-registered
// address is used), and an agent missing from the registry fails the stop.
func TestRoutingStopResolvesAtStopTime(t *testing.T) {
	A1 := newFakeAgent(t, http.StatusOK, `{"container_id":"cid"}`)
	A2 := newFakeAgent(t, http.StatusOK, `{}`)
	st := NewStore(time.Now)
	reg := NewRegistry(time.Now, HealthThresholds{})
	h := withControllerKey(newControllerServer(st, reg, newHTTPAgentClient(testKey, nil), testKey, testControllerKey))
	hb := hbFor("A")
	hb.Address = A1.URL
	postHeartbeat(h, strp(testKey), hbJSON(t, hb))
	w := doReq(h, http.MethodPost, "/servers", `{"agent_id":"A","name":"x","game":"g","config":{"image":"alpine"}}`)
	var srv shared.Server
	singleJSON(t, w.Body.Bytes(), &srv)
	waitState(t, h, srv.ID, shared.StateRunning)

	hb.Address = A2.URL
	hb.Containers = []shared.ContainerInfo{{ServerID: srv.ID, ContainerID: "cid", State: "running"}}
	postHeartbeat(h, strp(testKey), hbJSON(t, hb))
	doReq(h, http.MethodPost, "/servers/"+srv.ID+"/stop", "")
	waitState(t, h, srv.ID, shared.StateStopped)
	if _, s := agentHitsByKind(t, A2); len(s) != 1 || s[0] != srv.ID {
		t.Errorf("stop hits on new address = %v", s)
	}
	if _, s := agentHitsByKind(t, A1); len(s) != 0 {
		t.Errorf("stop hit old address: %v", s)
	}

	// Server bound to an agent not in the registry.
	req := createReq("ghost")
	req.AgentID = "ghost-agent"
	g, _ := st.Create(req)
	st.Transition(g.ID, shared.StateStarting, "", nil)
	st.Transition(g.ID, shared.StateRunning, "", nil)
	if w := doReq(h, http.MethodPost, "/servers/"+g.ID+"/stop", ""); w.Code != http.StatusAccepted {
		t.Fatalf("stop ghost: %d", w.Code)
	}
	got := waitState(t, h, g.ID, shared.StateFailed)
	if got.Message == "" {
		t.Error("failed stop has no message")
	}
}

// AC5: POST /servers agent validation.
func TestCreateServerAgentValidation(t *testing.T) {
	clk := newFakeClock()
	reg := NewRegistry(clk.Now, HealthThresholds{})
	st := NewStore(clk.Now)
	agent := &instantAgent{}
	h := withControllerKey(newControllerServer(st, reg, agent, testKey, testControllerKey))
	reg.Upsert(hbFor("sick"))
	reg.Upsert(hbFor("dead"))
	clk.Advance(defaultUnhealthyAfter + time.Second)
	reg.Upsert(hbFor("ok"))
	reg.Sweep()
	if a := mustGet(t, reg, "sick"); a.Status != shared.AgentUnhealthy {
		t.Fatalf("setup sick = %s", a.Status)
	}
	clk.Advance(defaultOfflineAfter)
	reg.Upsert(hbFor("ok"))
	reg.Upsert(hbFor("sick"))
	clk.Advance(defaultUnhealthyAfter + time.Second)
	reg.Upsert(hbFor("ok"))
	reg.Sweep()
	if a := mustGet(t, reg, "dead"); a.Status != shared.AgentOffline {
		t.Fatalf("setup dead = %s", a.Status)
	}
	if a := mustGet(t, reg, "sick"); a.Status != shared.AgentUnhealthy {
		t.Fatalf("setup sick = %s", a.Status)
	}

	body := func(agent string) string {
		return fmt.Sprintf(`{"agent_id":%q,"name":"mc1","game":"g","config":{"image":"alpine"}}`, agent)
	}
	cases := []struct {
		name, body string
		status     int
	}{
		{"missing agent_id", `{"name":"mc1","game":"g","config":{"image":"alpine"}}`, http.StatusBadRequest},
		{"empty agent_id", body(""), http.StatusBadRequest},
		{"unknown agent", body("nope"), http.StatusNotFound},
		{"unhealthy agent", body("sick"), http.StatusConflict},
		{"offline agent", body("dead"), http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertError(t, doReq(h, http.MethodPost, "/servers", tc.body), tc.status)
		})
	}
	if l := st.List(); len(l) != 0 {
		t.Errorf("rejected creates stored servers: %+v", l)
	}
	if n := agent.launches.Load(); n != 0 {
		t.Errorf("launched %d times", n)
	}
	if w := doReq(h, http.MethodPost, "/servers", body("ok")); w.Code != http.StatusAccepted {
		t.Errorf("healthy agent: %d %s", w.Code, w.Body)
	}
}

// AC7: the removed single-agent URL env var no longer appears in any Go
// file (the needle is split so this file does not match itself).
func TestNoSnivurAgentURLInCode(t *testing.T) {
	needle := "SNIVUR_AGENT" + "_URL"
	root := ".."
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skip("repo root not found")
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			idx := strings.Index(line, needle)
			if idx >= 0 {
				t.Errorf("%s:%d: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// AC8: concurrent heartbeats (with reconciliation), sweeps, creates, stops
// and lists from 60+ goroutines under -race, with a moving fake clock.
func TestConcurrentReconcileSweepCreateStop(t *testing.T) {
	clk := newFakeClock()
	st := NewStore(clk.Now)
	reg := NewRegistry(clk.Now, HealthThresholds{})
	agent := &instantAgent{}
	h := withControllerKey(newControllerServer(st, reg, agent, testKey, testControllerKey))
	agents := []string{"a0", "a1", "a2"}
	for _, a := range agents {
		postHeartbeat(h, strp(testKey), hbJSON(t, hbFor(a)))
	}

	const (
		creators    = 24
		heartbeats  = 18
		sweepers    = 6
		listers     = 6
		clockMovers = 2
		stoppers    = 6
	)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	errs := make(chan error, 1000)
	var created atomic.Int64

	for i := 0; i < creators; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			for j := 0; j < 5; j++ {
				a := agents[(i+j)%len(agents)]
				body := fmt.Sprintf(`{"agent_id":%q,"name":"s-%d-%d","game":"g","config":{"image":"alpine"}}`, a, i, j)
				w := doReq(h, http.MethodPost, "/servers", body)
				switch w.Code {
				case http.StatusAccepted:
					created.Add(1)
					var srv shared.Server
					if err := json.Unmarshal(w.Body.Bytes(), &srv); err != nil {
						errs <- err
						continue
					}
					doReq(h, http.MethodPost, "/servers/"+srv.ID+"/stop", "") // 202 or 409
				case http.StatusConflict: // agent went unhealthy/offline
				default:
					errs <- fmt.Errorf("create: %d %s", w.Code, w.Body)
				}
			}
		}(i)
	}
	for i := 0; i < heartbeats; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			rng := rand.New(rand.NewSource(int64(i)))
			a := agents[i%len(agents)]
			for j := 0; j < 30; j++ {
				hb := hbFor(a)
				hb.Containers = []shared.ContainerInfo{}
				for _, s := range st.List() {
					if s.AgentID == a && rng.Intn(2) == 0 {
						hb.Containers = append(hb.Containers, shared.ContainerInfo{ServerID: s.ID, ContainerID: "c", State: "running"})
					}
				}
				if rng.Intn(5) == 0 {
					hb.ContainersError = "boom"
				}
				if w := postHeartbeat(h, strp(testKey), hbJSON(t, hb)); w.Code != http.StatusNoContent {
					errs <- fmt.Errorf("heartbeat: %d", w.Code)
				}
			}
		}(i)
	}
	for i := 0; i < sweepers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			for j := 0; j < 50; j++ {
				handleStatusChanges(reg.Sweep(), st)
			}
		}()
	}
	for i := 0; i < clockMovers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			for j := 0; j < 50; j++ {
				clk.Advance(3 * time.Second)
				time.Sleep(100 * time.Microsecond)
			}
		}()
	}
	for i := 0; i < listers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			for j := 0; j < 30; j++ {
				for _, q := range []string{"", "?agent_id=a1"} {
					if w := doReq(h, http.MethodGet, "/servers"+q, ""); w.Code != http.StatusOK {
						errs <- fmt.Errorf("list: %d", w.Code)
					}
				}
				doReq(h, http.MethodGet, "/agents", "")
			}
		}()
	}
	for i := 0; i < stoppers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			for j := 0; j < 30; j++ {
				for _, s := range st.List() {
					if s.State == shared.StateRunning || s.State == shared.StateUnknown {
						doReq(h, http.MethodPost, "/servers/"+s.ID+"/stop", "")
					}
				}
			}
		}()
	}
	if n := creators + heartbeats + sweepers + clockMovers + listers + stoppers; n < 50 {
		t.Fatalf("only %d goroutines", n)
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	valid := map[shared.ServerState]bool{}
	for _, s := range allStates7 {
		valid[s] = true
	}
	// Let in-flight launch/stop goroutines finish.
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := false
		for _, s := range st.List() {
			if !valid[s.State] {
				t.Fatalf("invalid state %q", s.State)
			}
			if s.State == shared.StatePending || s.State == shared.StateStarting || s.State == shared.StateStopping {
				busy = true
			}
		}
		if !busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if created.Load() == 0 {
		t.Error("no servers created")
	}
}
