package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"snivur/v0/shared"
	"strings"
	"time"
)

// agentTimeout bounds controller->agent calls; docker run -d still pulls
// images synchronously, so this is generous.
const agentTimeout = 5 * time.Minute

// Config holds the controller's runtime configuration.
type Config struct {
	Addr        string // SNIVUR_CONTROLLER_ADDR
	AgentURL    string // SNIVUR_AGENT_URL
	AgentAPIKey string // SNIVUR_AGENT_API_KEY
}

// loadConfig reads configuration using getenv (normally os.Getenv).
func loadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:        getenv("SNIVUR_CONTROLLER_ADDR"),
		AgentURL:    getenv("SNIVUR_AGENT_URL"),
		AgentAPIKey: getenv("SNIVUR_AGENT_API_KEY"),
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.AgentURL == "" {
		cfg.AgentURL = "http://localhost:8000"
	}
	if cfg.AgentAPIKey == "" {
		return Config{}, errors.New("SNIVUR_AGENT_API_KEY is required")
	}
	return cfg, nil
}

// newAgentClient returns the HTTP client used to talk to agents.
func newAgentClient() *http.Client {
	return &http.Client{Timeout: agentTimeout}
}

// createServerRequest is the client-facing body for POST /servers.
type createServerRequest struct {
	Name   string            `json:"name"`
	Game   string            `json:"game"`
	Config map[string]string `json:"config"`
}

type controllerServer struct {
	cfg    Config
	client *http.Client
}

// newControllerServer builds the controller's HTTP handler. Requests are
// forwarded to cfg.AgentURL using client.
func newControllerServer(cfg Config, client *http.Client) http.Handler {
	s := &controllerServer{cfg: cfg, client: client}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /servers", s.createServer)
	return mux
}

func (s *controllerServer) createServer(w http.ResponseWriter, r *http.Request) {
	var body createServerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	req := shared.LaunchRequest{
		ServerID: shared.NewID(),
		Name:     body.Name,
		Game:     body.Game,
		Config:   body.Config,
	}
	if err := shared.ValidateLaunch(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload, err := json.Marshal(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode agent request: "+err.Error())
		return
	}
	url := strings.TrimRight(s.cfg.AgentURL, "/") + "/launch"
	agentReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build agent request: "+err.Error())
		return
	}
	agentReq.Header.Set("Content-Type", "application/json")
	agentReq.Header.Set("X-API-Key", s.cfg.AgentAPIKey)

	resp, err := s.client.Do(agentReq)
	if err != nil {
		log.Printf("contact agent for %s: %v", req.ServerID, err)
		writeError(w, http.StatusBadGateway, "failed to contact agent: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Pass the agent's status and body straight through.
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("relay agent response for %s: %v", req.ServerID, err)
	}
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
