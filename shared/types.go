package shared

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// LaunchRequest is sent from the controller to an agent to start a server.
type LaunchRequest struct {
	ServerID string            `json:"server_id"`
	Name     string            `json:"name"`
	Game     string            `json:"game"`
	Config   map[string]string `json:"config"` // "image" is required
}

// LaunchResponse is returned by an agent once the container has started.
type LaunchResponse struct {
	ServerID    string `json:"server_id"`
	ContainerID string `json:"container_id"`
}

// ErrorResponse is the error body used by every endpoint.
type ErrorResponse struct {
	Error string `json:"error"`
}

// NewID returns 16 random bytes from crypto/rand, hex-encoded (32 chars).
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand only fails if the OS entropy source is broken.
		panic(fmt.Sprintf("shared: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// ValidateLaunch checks user-supplied fields before they are passed to docker.
func ValidateLaunch(req LaunchRequest) error {
	if !namePattern.MatchString(req.Name) {
		return fmt.Errorf("invalid name %q: must match %s", req.Name, namePattern.String())
	}
	image := req.Config["image"]
	switch {
	case image == "":
		return errors.New("config.image is required")
	case strings.HasPrefix(image, "-"):
		return fmt.Errorf("invalid image %q: must not start with '-'", image)
	case strings.IndexFunc(image, unicode.IsSpace) >= 0:
		return fmt.Errorf("invalid image %q: must not contain whitespace", image)
	}
	return nil
}

// ServerState is the lifecycle state of a server tracked by the controller.
type ServerState string

const (
	StatePending  ServerState = "pending"
	StateStarting ServerState = "starting"
	StateRunning  ServerState = "running"
	StateStopping ServerState = "stopping"
	StateStopped  ServerState = "stopped"
	StateFailed   ServerState = "failed"
)

// Server is the controller's record of a game server.
type Server struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Game        string            `json:"game"`
	Config      map[string]string `json:"config"`
	State       ServerState       `json:"state"`
	Message     string            `json:"message,omitempty"` // last error / reason
	ContainerID string            `json:"container_id,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// CreateServerRequest is the client-facing body for POST /servers.
type CreateServerRequest struct {
	Name   string            `json:"name"`
	Game   string            `json:"game"`
	Config map[string]string `json:"config"`
}
