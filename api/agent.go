package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
)

// AgentClient handles interactions with the agent API
type AgentClient struct {
	BaseURL string
	APIKey  string
}

// NewAgentClient initializes a new AgentClient
func NewAgentClient(baseURL, apiKey string) *AgentClient {
	return &AgentClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
	}
}

// CallAgent sends a request to the agent API and decodes the response
func (c *AgentClient) CallAgent(path string, reqBody interface{}, respBody interface{}) (int, error) {
	// Build the request
	fullURL := c.BaseURL + path
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest("POST", fullURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	// Decode the response
	if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
		return resp.StatusCode, errors.New("failed to decode agent response: " + err.Error())
	}

	return resp.StatusCode, nil
}
