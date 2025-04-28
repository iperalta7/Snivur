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

func launchHandler(writer http.ResponseWriter, request *http.Request) {
	var req shared.LaunchRequest
	if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
		http.Error(writer, "Invalid request", http.StatusBadRequest)
		return
	}

	log.Printf("Launching %s server with config: %v", req.Game, req.Config)

	var server GameServer = DockerServer{}
	cmd := server.Run(req)

	err := cmd.Start()
	if err != nil {
		log.Printf("Error: %v", err)
		json.NewEncoder(writer).Encode(shared.LaunchResponse{Status: "error", Message: err.Error()})
		return
	}

	log.Printf("Waiting for command to finish...")
	err = cmd.Wait()
	if err != nil {
		log.Printf("Command finished with error: %v", err)
		json.NewEncoder(writer).Encode(shared.LaunchResponse{Status: "error", Message: err.Error()})
	} else {
		log.Printf("Command finished successfully")
	}

	json.NewEncoder(writer).Encode(shared.LaunchResponse{Status: "launching", Message: "Server is starting"})
}

func main() {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.URLFormat)
	shared.ApiKey = "agent"
	r.Use(shared.ApiKeyMiddleware)

	// Register routes
	r.Get("/health", health.HealthCheckHandler)
	r.Post("/launch", launchHandler)

	log.Println("Agent listening on :8081")
	log.Println("Agent API Key: ", shared.ApiKey)
	err := http.ListenAndServe(":8081", r)
	if err != nil {
		log.Fatalf("could not start server: %v", err)
	}
}
