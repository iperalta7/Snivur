package main

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"sync"
	"time"

	"snivur/v0/shared"
)

var (
	// ErrInvalidTransition is returned by Transition for a move the state
	// machine does not allow.
	ErrInvalidTransition = errors.New("invalid state transition")
	// ErrNameConflict is returned by Create when a non-terminal server
	// already uses the requested name.
	ErrNameConflict = errors.New("server name already in use")
	// ErrNotFound is returned by Transition for an unknown server ID.
	ErrNotFound = errors.New("server not found")
	// ErrStateChanged is returned by TransitionFrom when the server is no
	// longer in the expected state.
	ErrStateChanged = errors.New("server state changed")
)

// transitions lists every allowed (from -> to) move. stopped and failed are
// terminal and therefore have no entry. unknown is entered only when a
// running server's agent goes offline (ADR 0004) and is left through
// reconciliation; it cannot be stopped.
var transitions = map[shared.ServerState][]shared.ServerState{
	shared.StatePending:  {shared.StateStarting, shared.StateFailed},
	shared.StateStarting: {shared.StateRunning, shared.StateFailed},
	shared.StateRunning:  {shared.StateStopping, shared.StateFailed, shared.StateUnknown},
	shared.StateStopping: {shared.StateStopped, shared.StateFailed},
	shared.StateUnknown:  {shared.StateRunning, shared.StateFailed},
}

// canTransition reports whether from -> to is allowed.
func canTransition(from, to shared.ServerState) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// isTerminal reports whether no further transitions are possible from s.
func isTerminal(s shared.ServerState) bool {
	return s == shared.StateStopped || s == shared.StateFailed
}

// Store is the controller's in-memory record of servers (desired state).
// All methods are safe for concurrent use and return copies.
type Store struct {
	mu      sync.RWMutex
	servers map[string]*shared.Server
	order   []string // IDs in insertion order, tie-breaker for List
	now     func() time.Time
}

// NewStore returns an empty store that timestamps changes using now.
// A nil now defaults to time.Now.
func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{servers: make(map[string]*shared.Server), now: now}
}

func clone(s *shared.Server) shared.Server {
	c := *s
	c.Config = maps.Clone(s.Config)
	return c
}

// Create records a new server in the pending state with a fresh ID.
// It returns ErrNameConflict if a non-terminal server has the same name.
func (s *Store) Create(req shared.CreateServerRequest) (shared.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.servers {
		if existing.Name == req.Name && !isTerminal(existing.State) {
			return shared.Server{}, fmt.Errorf("%w: %q", ErrNameConflict, req.Name)
		}
	}
	now := s.now()
	srv := &shared.Server{
		ID:        shared.NewID(),
		AgentID:   req.AgentID,
		Name:      req.Name,
		Game:      req.Game,
		Config:    maps.Clone(req.Config),
		State:     shared.StatePending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.servers[srv.ID] = srv
	s.order = append(s.order, srv.ID)
	return clone(srv), nil
}

// Get returns a copy of the server with the given ID.
func (s *Store) Get(id string) (shared.Server, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	srv, ok := s.servers[id]
	if !ok {
		return shared.Server{}, false
	}
	return clone(srv), true
}

// List returns copies of all servers sorted by CreatedAt ascending (ties
// keep insertion order). It never returns nil.
func (s *Store) List() []shared.Server {
	s.mu.RLock()
	out := make([]shared.Server, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, clone(s.servers[id]))
	}
	s.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Transition moves server id to state to, setting Message to msg and
// UpdatedAt to now, and applying mutate (if non-nil) under the lock.
// mutate cannot override ID, State, Message, CreatedAt or UpdatedAt.
// It returns ErrNotFound for an unknown ID and ErrInvalidTransition for a
// move the state machine forbids; in both cases nothing is changed.
func (s *Store) Transition(id string, to shared.ServerState, msg string, mutate func(*shared.Server)) (shared.Server, error) {
	return s.transition(id, "", to, msg, mutate)
}

// TransitionFrom is Transition with a compare-and-set: if the server's
// current state is not from, it returns ErrStateChanged and changes
// nothing.
func (s *Store) TransitionFrom(id string, from, to shared.ServerState, msg string, mutate func(*shared.Server)) (shared.Server, error) {
	if from == "" {
		return shared.Server{}, fmt.Errorf("%w: empty from state", ErrStateChanged)
	}
	return s.transition(id, from, to, msg, mutate)
}

// transition implements Transition and TransitionFrom under the lock. An
// empty from skips the compare-and-set check.
func (s *Store) transition(id string, from, to shared.ServerState, msg string, mutate func(*shared.Server)) (shared.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv, ok := s.servers[id]
	if !ok {
		return shared.Server{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if from != "" && srv.State != from {
		return clone(srv), fmt.Errorf("%w: %s is %s, expected %s", ErrStateChanged, id, srv.State, from)
	}
	if !canTransition(srv.State, to) {
		return clone(srv), fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, srv.State, to)
	}
	if mutate != nil {
		// Mutate a copy so a misbehaving callback cannot retain a pointer
		// into the store or change the fields the state machine owns.
		c := clone(srv)
		mutate(&c)
		c.ID, c.CreatedAt = srv.ID, srv.CreatedAt
		c.Config = maps.Clone(c.Config)
		*srv = c
	}
	srv.State = to
	srv.Message = msg
	srv.UpdatedAt = s.now()
	return clone(srv), nil
}
