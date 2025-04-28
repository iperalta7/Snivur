package main

import (
	"encoding/json"
	"log"
	"net/http"
	"snivur/v0/shared"
	"snivur/v0/shared/health"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
)

var (
	nodes = make(map[string]*AgentClient)
)

func reqJson(r *http.Request) (shared.LaunchRequest, error) {
	var req shared.LaunchRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		return req, err
	}
	return req, nil
}

func createServer(w http.ResponseWriter, r *http.Request) {
	req, err := reqJson(r)
	if err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	log.Printf("Received request to create server: %v", req)

	// Use AgentClient to interact with the agent API

	var agentResp shared.LaunchResponse
	statusCode, err := nodes["agent"].CallAgent("/launch", req, &agentResp)
	if err != nil {
		http.Error(w, "Failed to communicate with agent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(statusCode)
	if statusCode != http.StatusOK {
		agentResp.Message = "Error from agent: " + agentResp.Message
	}
	json.NewEncoder(w).Encode(agentResp)
}

func main() {
	r := chi.NewRouter()
	nodes["agent"] = NewAgentClient("http://localhost:8081", "agent") // TODO make this dynamic and persist

	// Register routes
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.URLFormat)
	r.Use(shared.ApiKeyMiddleware)

	r.Get("/health", health.HealthCheckHandler)
	r.Post("/create", createServer)

	log.Println("API listening on :8080")
	log.Println("API Key: ", shared.ApiKey)
	err := http.ListenAndServe(":8080", r)
	if err != nil {
		log.Fatalf("could not start server: %v", err)
	}
}
