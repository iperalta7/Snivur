package main

import (
	"encoding/json"
	"log"
	"net/http"
	"snivur/v0/shared"
)

func apiKeyMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		key := request.Header.Get("X-API-Key")
		if key != "test-secret" { // TODO: better auth
			err := "Invalid API key"
			writer.WriteHeader(http.StatusUnauthorized) // Set the status code
			json.NewEncoder(writer).Encode(shared.LaunchResponse{
				Status:  "error",
				Message: err,
			})
			return
		}
		next(writer, request)
	}
}

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
	}
	log.Printf("command to finish...")

	json.NewEncoder(writer).Encode(shared.LaunchResponse{Status: "launching", Message: "Server is starting"})
}

func main() {
	http.HandleFunc("/launch", apiKeyMiddleware(launchHandler))
	log.Println("Agent listening on :8000")
	http.ListenAndServe(":8000", nil)
}
