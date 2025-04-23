package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"snivur/v0/shared"
)

func createServer(w http.ResponseWriter, r *http.Request) {
	var req shared.LaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid input", http.StatusBadRequest)
		return
	}

	agentURL := "http://localhost:8000/launch" // TODO: better agent
	body, _ := json.Marshal(req)

	reqToAgent, err := http.NewRequest("POST", agentURL, bytes.NewBuffer(body))
	if err != nil {
		http.Error(w, "Failed to build agent request", 500)
		return
	}
	reqToAgent.Header.Set("Content-Type", "application/json")
	reqToAgent.Header.Set("X-API-Key", req.AgentApiKey)

	client := &http.Client{}
	resp, err := client.Do(reqToAgent)
	if err != nil {
		http.Error(w, "Failed to contact agent", 500)
		return
	}
	defer resp.Body.Close()

	var agentResp shared.LaunchResponse
	if err := json.NewDecoder(resp.Body).Decode(&agentResp); err != nil {
		http.Error(w, "Failed to parse agent response", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(resp.StatusCode)
	json.NewEncoder(w).Encode(agentResp)
}

func main() {
	http.HandleFunc("/servers", createServer)
	log.Println("Controller running on :8080")
	http.ListenAndServe(":8080", nil)
}
