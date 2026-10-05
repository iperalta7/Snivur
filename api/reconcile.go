package main

import (
	"sort"
	"time"

	"snivur/v0/shared"
)

// Transition is a state change the reconciler wants applied to a server.
type Transition struct {
	ServerID string
	From     shared.ServerState // state Reconcile saw; applied with TransitionFrom
	To       shared.ServerState
	Message  string
}

// reconcileGrace is how long after its last update a running server is
// exempt from reconciliation, so a heartbeat whose container list predates
// the launch cannot fail a server that just started. It is longer than one
// heartbeat interval. A var so tests can shorten it.
var reconcileGrace = 15 * time.Second

// Reconcile compares the servers bound to hb.AgentID (desired state) with
// the containers hb reports (actual state). It returns the transitions to
// apply, sorted by ServerID, and the orphans: running containers with no
// matching server on that agent, or whose server is stopped or failed,
// sorted by ServerID then ContainerID.
//
// Running servers whose UpdatedAt is within reconcileGrace of now are
// skipped. If hb.ContainersError is set it returns nothing: a failed
// container listing is not evidence that containers died.
//
// Reconcile is pure: no locks, no I/O. now is the controller clock.
func Reconcile(servers []shared.Server, hb shared.Heartbeat, now time.Time) (transitions []Transition, orphans []shared.ContainerInfo) {
	if hb.ContainersError != "" {
		return nil, nil
	}

	running := make(map[string]bool, len(hb.Containers))
	for _, c := range hb.Containers {
		if c.State == "running" {
			running[c.ServerID] = true
		}
	}

	byID := make(map[string]shared.Server)
	for _, srv := range servers {
		if srv.AgentID != hb.AgentID {
			continue
		}
		byID[srv.ID] = srv
		up := running[srv.ID]
		switch {
		case srv.State == shared.StateRunning && !up && now.Sub(srv.UpdatedAt) > reconcileGrace:
			transitions = append(transitions, Transition{srv.ID, srv.State, shared.StateFailed, "container not running on agent"})
		case srv.State == shared.StateUnknown && up:
			transitions = append(transitions, Transition{srv.ID, srv.State, shared.StateRunning, "agent recovered"})
		case srv.State == shared.StateUnknown && !up:
			transitions = append(transitions, Transition{srv.ID, srv.State, shared.StateFailed, "container not found after agent recovered"})
		}
	}

	for _, c := range hb.Containers {
		if c.State != "running" {
			continue
		}
		if srv, ok := byID[c.ServerID]; !ok || isTerminal(srv.State) {
			orphans = append(orphans, c)
		}
	}

	sort.Slice(transitions, func(i, j int) bool { return transitions[i].ServerID < transitions[j].ServerID })
	sort.Slice(orphans, func(i, j int) bool {
		if orphans[i].ServerID != orphans[j].ServerID {
			return orphans[i].ServerID < orphans[j].ServerID
		}
		return orphans[i].ContainerID < orphans[j].ContainerID
	})
	return transitions, orphans
}
