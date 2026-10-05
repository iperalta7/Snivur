package main

import (
	"context"
	"log"
	"slices"
	"sort"
	"sync"
	"time"

	"snivur/v0/shared"
)

const (
	// defaultUnhealthyAfter is how long after its last heartbeat an agent
	// stops being healthy.
	defaultUnhealthyAfter = 30 * time.Second
	// defaultOfflineAfter is how long after its last heartbeat an agent is
	// considered offline.
	defaultOfflineAfter = 90 * time.Second
	// sweepInterval is how often the controller recomputes agent health.
	sweepInterval = 5 * time.Second
)

// HealthThresholds maps time since the last heartbeat to an AgentStatus:
// <= Unhealthy is healthy, <= Offline is unhealthy, beyond that offline.
// Zero fields take the defaults (30s, 90s).
type HealthThresholds struct{ Unhealthy, Offline time.Duration }

// DefaultHealthThresholds returns the ADR 0003 thresholds (30s, 90s).
func DefaultHealthThresholds() HealthThresholds {
	return HealthThresholds{Unhealthy: defaultUnhealthyAfter, Offline: defaultOfflineAfter}
}

// StatusChange records an agent whose status changed during a Sweep.
type StatusChange struct {
	AgentID  string
	From, To shared.AgentStatus
}

// Registry is the controller's in-memory record of agents, built from
// heartbeats. All methods are safe for concurrent use and return copies.
type Registry struct {
	mu         sync.RWMutex
	agents     map[string]*shared.Agent
	now        func() time.Time
	thresholds HealthThresholds
}

// NewRegistry returns an empty registry. A nil now defaults to time.Now and
// zero thresholds default to DefaultHealthThresholds.
func NewRegistry(now func() time.Time, t HealthThresholds) *Registry {
	if now == nil {
		now = time.Now
	}
	def := DefaultHealthThresholds()
	if t.Unhealthy <= 0 {
		t.Unhealthy = def.Unhealthy
	}
	if t.Offline <= 0 {
		t.Offline = def.Offline
	}
	return &Registry{agents: make(map[string]*shared.Agent), now: now, thresholds: t}
}

// cloneContainers copies cs, never returning nil.
func cloneContainers(cs []shared.ContainerInfo) []shared.ContainerInfo {
	if cs == nil {
		return []shared.ContainerInfo{}
	}
	return slices.Clone(cs)
}

func cloneAgent(a *shared.Agent) shared.Agent {
	c := *a
	c.Containers = cloneContainers(a.Containers)
	return c
}

// Upsert records a heartbeat, creating the agent if needed. It sets
// LastSeen to the registry clock (not hb.SentAt) and Status to healthy.
func (r *Registry) Upsert(hb shared.Heartbeat) shared.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := &shared.Agent{
		ID:              hb.AgentID,
		Hostname:        hb.Hostname,
		Address:         hb.Address,
		Version:         hb.Version,
		Status:          shared.AgentHealthy,
		LastSeen:        r.now(),
		Containers:      cloneContainers(hb.Containers),
		ContainersError: hb.ContainersError,
	}
	r.agents[a.ID] = a
	return cloneAgent(a)
}

// Get returns a copy of the agent with the given ID.
func (r *Registry) Get(id string) (shared.Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[id]
	if !ok {
		return shared.Agent{}, false
	}
	return cloneAgent(a), true
}

// List returns copies of all agents sorted by ID. It never returns nil.
func (r *Registry) List() []shared.Agent {
	r.mu.RLock()
	out := make([]shared.Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, cloneAgent(a))
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// statusFor derives an agent's status from the time since it was last seen.
func (r *Registry) statusFor(since time.Duration) shared.AgentStatus {
	switch {
	case since <= r.thresholds.Unhealthy:
		return shared.AgentHealthy
	case since <= r.thresholds.Offline:
		return shared.AgentUnhealthy
	default:
		return shared.AgentOffline
	}
}

// Sweep recomputes every agent's status from the registry clock and returns
// the agents whose status changed, sorted by AgentID. It never returns nil.
func (r *Registry) Sweep() []StatusChange {
	r.mu.Lock()
	now := r.now()
	changes := []StatusChange{}
	for _, a := range r.agents {
		to := r.statusFor(now.Sub(a.LastSeen))
		if to != a.Status {
			changes = append(changes, StatusChange{AgentID: a.ID, From: a.Status, To: to})
			a.Status = to
		}
	}
	r.mu.Unlock()
	sort.Slice(changes, func(i, j int) bool { return changes[i].AgentID < changes[j].AgentID })
	return changes
}

// runSweeper calls reg.Sweep every interval and passes the changes to
// handleStatusChanges until ctx is cancelled.
func runSweeper(ctx context.Context, reg *Registry, store *Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			handleStatusChanges(reg.Sweep(), store)
		}
	}
}

// handleStatusChanges logs each agent status change and, for every agent
// that went offline, moves its running servers to unknown. A server that
// changed state concurrently is logged and skipped.
func handleStatusChanges(changes []StatusChange, store *Store) {
	for _, c := range changes {
		log.Printf("agent %s: %s -> %s", c.AgentID, c.From, c.To)
		if c.To != shared.AgentOffline {
			continue
		}
		for _, srv := range store.List() {
			if srv.AgentID != c.AgentID || srv.State != shared.StateRunning {
				continue
			}
			if _, err := store.TransitionFrom(srv.ID, shared.StateRunning, shared.StateUnknown, "agent offline", nil); err != nil {
				log.Printf("agent %s offline: server %s -> %s: %v (ignored)", c.AgentID, srv.ID, shared.StateUnknown, err)
				continue
			}
			log.Printf("agent %s offline: server %s -> %s", c.AgentID, srv.ID, shared.StateUnknown)
		}
	}
}
