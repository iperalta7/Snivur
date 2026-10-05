package shared

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
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
