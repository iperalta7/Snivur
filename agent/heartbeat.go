package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"snivur/v0/shared"
)

const (
	// defaultHeartbeatInterval is used when Heartbeater.Interval is unset.
	defaultHeartbeatInterval = 10 * time.Second
	// heartbeatTimeout bounds each heartbeat HTTP call.
	heartbeatTimeout = 5 * time.Second
	// maxHeartbeatResponseBody bounds how much of a response is read.
	maxHeartbeatResponseBody = 64 << 10
)

// listTimeout bounds each Runtime.List call so a hung docker ps cannot stop
// heartbeats. It is a var so tests can shorten it.
var listTimeout = 5 * time.Second

// Identity is what an agent reports about itself in every heartbeat.
type Identity struct {
	AgentID  string
	Hostname string
	Address  string // base URL the controller uses to reach this agent
	Version  string
}

// Heartbeater periodically pushes a shared.Heartbeat to the controller.
type Heartbeater struct {
	ControllerURL string
	APIKey        string
	Interval      time.Duration // <= 0 means defaultHeartbeatInterval
	Runtime       Runtime
	Client        *http.Client // nil means newHeartbeatClient()
	Identity      Identity
}

// newHeartbeatClient returns the HTTP client used for heartbeats.
func newHeartbeatClient() *http.Client {
	return &http.Client{Timeout: heartbeatTimeout}
}

// Run sends one heartbeat immediately and then one per Interval until ctx is
// cancelled. Failures are logged and retried on the next tick; Run never
// panics or exits early.
func (h *Heartbeater) Run(ctx context.Context) {
	interval := h.Interval
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	client := h.Client
	if client == nil {
		client = newHeartbeatClient()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		h.beat(ctx, client)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// beat builds and sends a single heartbeat, logging any failure.
func (h *Heartbeater) beat(ctx context.Context, client *http.Client) {
	if ctx.Err() != nil {
		return
	}
	containers, listErr := h.listContainers(ctx)
	hb := shared.Heartbeat{
		AgentID:         h.Identity.AgentID,
		Hostname:        h.Identity.Hostname,
		Address:         h.Identity.Address,
		Version:         h.Identity.Version,
		Containers:      containers,
		ContainersError: listErr,
		SentAt:          time.Now().UTC(),
	}
	if err := h.send(ctx, client, hb); err != nil && ctx.Err() == nil {
		log.Printf("heartbeat: %v", err)
	}
}

// listContainers calls Runtime.List under its own listTimeout. On failure it
// logs and returns an empty slice plus the error text; on success the error
// text is empty. The slice is never nil.
func (h *Heartbeater) listContainers(ctx context.Context) ([]shared.ContainerInfo, string) {
	if h.Runtime == nil {
		return []shared.ContainerInfo{}, ""
	}
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	list, err := h.Runtime.List(lctx)
	if err == nil && lctx.Err() != nil {
		// A List that returned after its deadline is not trustworthy.
		err = lctx.Err()
	}
	if err != nil {
		log.Printf("heartbeat: list containers: %v", err)
		return []shared.ContainerInfo{}, err.Error()
	}
	if list == nil {
		list = []shared.ContainerInfo{}
	}
	return list, ""
}

func (h *Heartbeater) send(ctx context.Context, client *http.Client, hb shared.Heartbeat) error {
	payload, err := json.Marshal(hb)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	url := strings.TrimRight(h.ControllerURL, "/") + "/agents/heartbeat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHeartbeatResponseBody))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("controller returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

// advertiseWarning returns a warning when SNIVUR_AGENT_ADVERTISE_URL is
// unset (so the agent advertises http://localhost:<port>) but
// SNIVUR_CONTROLLER_URL points at a non-loopback host, which therefore
// cannot reach the agent. It returns "" when there is nothing to warn about,
// including when heartbeats are disabled.
func advertiseWarning(getenv func(string) string) string {
	controller := getenv("SNIVUR_CONTROLLER_URL")
	if getenv("SNIVUR_AGENT_ADVERTISE_URL") != "" || controller == "" {
		return ""
	}
	if u, err := url.Parse(controller); err == nil && isLoopbackHost(u.Hostname()) {
		return ""
	}
	return "SNIVUR_AGENT_ADVERTISE_URL is unset, so this agent advertises a localhost address, " +
		"which the controller at " + controller + " cannot reach; set SNIVUR_AGENT_ADVERTISE_URL"
}

// isLoopbackHost reports whether host is "localhost" or a loopback IP.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
