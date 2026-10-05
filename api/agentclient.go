package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"snivur/v0/shared"
)

// AgentClient is how the controller drives an agent.
type AgentClient interface {
	Launch(ctx context.Context, req shared.LaunchRequest) (shared.LaunchResponse, error)
	Stop(ctx context.Context, serverID string) error // returns nil if agent says 404 (already gone)
}

// maxAgentErrorBody bounds how much of an agent error body is read.
const maxAgentErrorBody = 64 << 10

// httpAgentClient talks to a single agent over HTTP.
type httpAgentClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// newHTTPAgentClient returns an AgentClient for the agent at baseURL,
// authenticating with apiKey. A nil client uses newAgentClient().
func newHTTPAgentClient(baseURL, apiKey string, client *http.Client) *httpAgentClient {
	if client == nil {
		client = newAgentClient()
	}
	return &httpAgentClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: client}
}

func (c *httpAgentClient) do(ctx context.Context, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode agent request: %w", err)
		}
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, rd)
	if err != nil {
		return nil, fmt.Errorf("build agent request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact agent: %w", err)
	}
	return resp, nil
}

// agentError builds an error from a non-success agent response, preferring
// the ErrorResponse message when the body has one.
func agentError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxAgentErrorBody))
	var er shared.ErrorResponse
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &er) == nil && er.Error != "" {
		msg = er.Error
	}
	return fmt.Errorf("agent returned %d: %s", resp.StatusCode, msg)
}

// Launch calls POST /launch on the agent.
func (c *httpAgentClient) Launch(ctx context.Context, req shared.LaunchRequest) (shared.LaunchResponse, error) {
	resp, err := c.do(ctx, "/launch", req)
	if err != nil {
		return shared.LaunchResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return shared.LaunchResponse{}, agentError(resp)
	}
	var lr shared.LaunchResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return shared.LaunchResponse{}, fmt.Errorf("decode agent response: %w", err)
	}
	return lr, nil
}

// Stop calls POST /servers/{id}/stop on the agent. A 404 means the
// container is already gone and is treated as success.
func (c *httpAgentClient) Stop(ctx context.Context, serverID string) error {
	resp, err := c.do(ctx, "/servers/"+url.PathEscape(serverID)+"/stop", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	default:
		return agentError(resp)
	}
}
