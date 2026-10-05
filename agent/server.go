package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"snivur/v0/shared"
	"time"
)

const (
	// maxBodyBytes caps every JSON request body.
	maxBodyBytes = 1 << 20
	// readHeaderTimeout bounds how long a client may take to send headers.
	readHeaderTimeout = 10 * time.Second
)

// Config holds the agent's runtime configuration.
type Config struct {
	Addr              string        // SNIVUR_AGENT_ADDR
	APIKey            string        // SNIVUR_AGENT_API_KEY
	AgentID           string        // SNIVUR_AGENT_ID, default os.Hostname()
	ControllerURL     string        // SNIVUR_CONTROLLER_URL, empty disables heartbeats
	AdvertiseURL      string        // SNIVUR_AGENT_ADVERTISE_URL
	HeartbeatInterval time.Duration // SNIVUR_HEARTBEAT_INTERVAL, default 10s
}

// loadConfig reads configuration using getenv (normally os.Getenv).
func loadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:          getenv("SNIVUR_AGENT_ADDR"),
		APIKey:        getenv("SNIVUR_AGENT_API_KEY"),
		AgentID:       getenv("SNIVUR_AGENT_ID"),
		ControllerURL: getenv("SNIVUR_CONTROLLER_URL"),
		AdvertiseURL:  getenv("SNIVUR_AGENT_ADVERTISE_URL"),
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8000"
	}
	if cfg.APIKey == "" {
		return Config{}, errors.New("SNIVUR_AGENT_API_KEY is required")
	}
	if cfg.AgentID == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			return Config{}, fmt.Errorf("SNIVUR_AGENT_ID is unset and hostname is unavailable: %v", err)
		}
		cfg.AgentID = host
	}
	if cfg.AdvertiseURL == "" {
		_, port, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			return Config{}, fmt.Errorf("SNIVUR_AGENT_ADVERTISE_URL is unset and SNIVUR_AGENT_ADDR %q has no port: %v", cfg.Addr, err)
		}
		cfg.AdvertiseURL = "http://localhost:" + port
	}
	cfg.HeartbeatInterval = defaultHeartbeatInterval
	if v := getenv("SNIVUR_HEARTBEAT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("SNIVUR_HEARTBEAT_INTERVAL: %v", err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("SNIVUR_HEARTBEAT_INTERVAL must be positive, got %s", v)
		}
		cfg.HeartbeatInterval = d
	}
	return cfg, nil
}

// newHTTPServer wraps handler in an http.Server with header timeouts set.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
}

type agentServer struct {
	cfg     Config
	runtime Runtime
}

// newAgentServer builds the agent's HTTP handler.
func newAgentServer(cfg Config, rt Runtime) http.Handler {
	s := &agentServer{cfg: cfg, runtime: rt}
	mux := http.NewServeMux()
	mux.Handle("POST /launch", s.requireAPIKey(http.HandlerFunc(s.launch)))
	mux.Handle("POST /servers/{id}/stop", s.requireAPIKey(http.HandlerFunc(s.stop)))
	return mux
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
	if status, err := decodeJSON(w, r, &req); err != nil {
		writeError(w, status, err.Error())
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

func (s *agentServer) stop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	log.Printf("stopping server %s", id)
	err := s.runtime.Stop(r.Context(), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "no container for server "+id)
	default:
		log.Printf("stop %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, err.Error())
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
