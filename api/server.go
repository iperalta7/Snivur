package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"snivur/v0/shared"
	"snivur/v0/shared/health"

	"github.com/go-chi/chi/v5"
)

const (
	// agentTimeout is a backstop on every controller->agent HTTP call;
	// per-operation contexts below are the primary bound.
	agentTimeout = 5 * time.Minute
	// launchTimeout bounds a background launch (docker run -d still pulls
	// images synchronously).
	launchTimeout = 5 * time.Minute
	// stopTimeout bounds a background stop.
	stopTimeout = 1 * time.Minute
	// maxBodyBytes caps every JSON request body.
	maxBodyBytes = 1 << 20
	// readHeaderTimeout bounds how long a client may take to send headers.
	readHeaderTimeout = 10 * time.Second
)

// Config holds the controller's runtime configuration.
type Config struct {
	Addr        string // SNIVUR_CONTROLLER_ADDR
	AgentURL    string // SNIVUR_AGENT_URL
	AgentAPIKey string // SNIVUR_AGENT_API_KEY

	// ControllerAPIKey (SNIVUR_CONTROLLER_API_KEY) is required on every
	// controller route. It is separate from AgentAPIKey so that holding an
	// agent's key does not grant control of the whole fleet.
	ControllerAPIKey string
}

// loadConfig reads configuration using getenv (normally os.Getenv).
func loadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:        getenv("SNIVUR_CONTROLLER_ADDR"),
		AgentURL:    getenv("SNIVUR_AGENT_URL"),
		AgentAPIKey: getenv("SNIVUR_AGENT_API_KEY"),

		ControllerAPIKey: getenv("SNIVUR_CONTROLLER_API_KEY"),
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.AgentURL == "" {
		cfg.AgentURL = "http://localhost:8081"
	}
	if cfg.AgentAPIKey == "" {
		return Config{}, errors.New("SNIVUR_AGENT_API_KEY is required")
	}
	if cfg.ControllerAPIKey == "" {
		return Config{}, errors.New("SNIVUR_CONTROLLER_API_KEY is required")
	}
	return cfg, nil
}

// newAgentClient returns the HTTP client used to talk to agents.
func newAgentClient() *http.Client {
	return &http.Client{Timeout: agentTimeout}
}

// newHTTPServer wraps handler in an http.Server with header timeouts set.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
}

type controllerServer struct {
	store  *Store
	agents AgentClient
}

// newControllerServer builds the controller's HTTP handler. Server state
// lives in store; lifecycle work is sent to agents in the background. Every
// route requires controllerKey.
func newControllerServer(store *Store, agents AgentClient, controllerKey string) http.Handler {
	s := &controllerServer{store: store, agents: agents}
	r := chi.NewRouter()
	r.Use(requireAPIKey(controllerKey))
	r.Get("/health", health.HealthCheckHandler)
	r.Post("/servers", s.createServer)
	r.Get("/servers", s.listServers)
	r.Get("/servers/{id}", s.getServer)
	r.Post("/servers/{id}/stop", s.stopServer)
	return r
}

// requireAPIKey rejects requests whose X-API-Key header does not match key,
// using a constant-time comparison.
func requireAPIKey(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-API-Key")
			if got == "" || key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
				writeError(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *controllerServer) createServer(w http.ResponseWriter, r *http.Request) {
	var body shared.CreateServerRequest
	if status, err := decodeJSON(w, r, &body); err != nil {
		writeError(w, status, err.Error())
		return
	}
	if err := shared.ValidateLaunch(shared.LaunchRequest{Name: body.Name, Game: body.Game, Config: body.Config}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	srv, err := s.store.Create(body)
	if errors.Is(err, ErrNameConflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Location", "/servers/"+srv.ID)
	writeJSON(w, http.StatusAccepted, srv)
	go s.launch(srv)
}

// launch drives a pending server to running (or failed). It runs on a
// background context so a disconnecting client cannot cancel it.
func (s *controllerServer) launch(srv shared.Server) {
	if _, err := s.store.Transition(srv.ID, shared.StateStarting, "", nil); err != nil {
		log.Printf("launch %s: %v", srv.ID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
	defer cancel()

	resp, err := s.agents.Launch(ctx, shared.LaunchRequest{
		ServerID: srv.ID,
		Name:     srv.Name,
		Game:     srv.Game,
		Config:   srv.Config, // srv is the store's copy; safe to hand off
	})
	if err != nil {
		log.Printf("launch %s failed: %v", srv.ID, err)
		s.transitionOrLog(srv.ID, shared.StateFailed, err.Error(), nil)
		return
	}
	s.transitionOrLog(srv.ID, shared.StateRunning, "", func(p *shared.Server) {
		p.ContainerID = resp.ContainerID
	})
}

func (s *controllerServer) listServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.List())
}

func (s *controllerServer) getServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

func (s *controllerServer) stopServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// running -> stopping is the only legal way into stopping, so the
	// store's transition check enforces "only when running" atomically.
	srv, err := s.store.Transition(id, shared.StateStopping, "", nil)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "server not found")
		return
	case errors.Is(err, ErrInvalidTransition):
		writeError(w, http.StatusConflict, "server is "+string(srv.State)+", not running")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, srv)
	go s.stop(id)
}

// stop drives a stopping server to stopped (or failed).
func (s *controllerServer) stop(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := s.agents.Stop(ctx, id); err != nil {
		log.Printf("stop %s failed: %v", id, err)
		s.transitionOrLog(id, shared.StateFailed, err.Error(), nil)
		return
	}
	s.transitionOrLog(id, shared.StateStopped, "", nil)
}

func (s *controllerServer) transitionOrLog(id string, to shared.ServerState, msg string, mutate func(*shared.Server)) {
	if _, err := s.store.Transition(id, to, msg, mutate); err != nil {
		log.Printf("server %s -> %s: %v", id, to, err)
	}
}

// decodeJSON decodes a size-limited JSON body into v. On failure it returns
// the HTTP status to use: 413 for an oversized body, 400 otherwise.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) (int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil {
		// Drain the rest so an oversized body is rejected even when a
		// complete JSON value appears within the limit.
		_, err = io.Copy(io.Discard, r.Body)
		if err == nil {
			return 0, nil
		}
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return http.StatusRequestEntityTooLarge, errors.New("request body too large")
	}
	return http.StatusBadRequest, errors.New("invalid JSON: " + err.Error())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, shared.ErrorResponse{Error: msg})
}
