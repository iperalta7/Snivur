package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"snivur/v0/shared"
	"snivur/v0/shared/health"

	"github.com/go-chi/chi/v5"
)

// Config holds the agent's runtime configuration.
type Config struct {
	Addr   string // SNIVUR_AGENT_ADDR
	APIKey string // SNIVUR_AGENT_API_KEY
}

// loadConfig reads configuration using getenv (normally os.Getenv).
func loadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:   getenv("SNIVUR_AGENT_ADDR"),
		APIKey: getenv("SNIVUR_AGENT_API_KEY"),
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8081"
	}
	if cfg.APIKey == "" {
		return Config{}, errors.New("SNIVUR_AGENT_API_KEY is required")
	}
	return cfg, nil
}

type agentServer struct {
	cfg     Config
	runtime Runtime
}

// newAgentServer builds the agent's HTTP handler.
func newAgentServer(cfg Config, rt Runtime) http.Handler {
	s := &agentServer{cfg: cfg, runtime: rt}
	r := chi.NewRouter()
	r.Use(s.requireAPIKey)
	r.Get("/health", health.HealthCheckHandler)
	r.Post("/launch", s.launch)
	return r
}

func (s *agentServer) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *agentServer) launch(w http.ResponseWriter, r *http.Request) {
	var req shared.LaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ServerID == "" {
		writeError(w, http.StatusBadRequest, "server_id is required")
		return
	}
	if err := shared.ValidateLaunch(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	log.Printf("launching server %s (%s) image=%s", req.ServerID, req.Name, req.Config["image"])
	containerID, err := s.runtime.Start(r.Context(), req)
	if err != nil {
		log.Printf("launch %s failed: %v", req.ServerID, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, shared.LaunchResponse{ServerID: req.ServerID, ContainerID: containerID})
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
