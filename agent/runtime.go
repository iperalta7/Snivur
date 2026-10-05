package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"snivur/v0/shared"
	"strings"
)

// ErrNotFound is returned by Runtime.Stop when no container carries the
// server's label.
var ErrNotFound = errors.New("container not found")

// Runtime starts and stops game server containers.
type Runtime interface {
	Start(ctx context.Context, req shared.LaunchRequest) (containerID string, err error)
	Stop(ctx context.Context, serverID string) error
}

// CommandRunner executes a command and returns combined stdout/stderr.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner is the real CommandRunner backed by os/exec.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// DockerRuntime runs containers via the docker CLI.
type DockerRuntime struct{ Run CommandRunner }

// Start launches a detached container labelled with the server ID and
// returns its container ID.
func (d DockerRuntime) Start(ctx context.Context, req shared.LaunchRequest) (string, error) {
	out, err := d.Run(ctx, "docker",
		"run", "-d", "--rm",
		"--name", req.Name,
		"--label", "snivur.server_id="+req.ServerID,
		req.Config["image"],
	)
	trimmed := string(bytes.TrimSpace(out))
	if err != nil {
		return "", fmt.Errorf("docker run: %w: %s", err, trimmed)
	}
	return trimmed, nil
}

// Stop finds the container(s) labelled with serverID and stops them. It
// returns ErrNotFound if none exist.
func (d DockerRuntime) Stop(ctx context.Context, serverID string) error {
	out, err := d.Run(ctx, "docker", "ps", "-q", "--filter", "label=snivur.server_id="+serverID)
	if err != nil {
		return fmt.Errorf("docker ps: %w: %s", err, bytes.TrimSpace(out))
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return ErrNotFound
	}
	out, err = d.Run(ctx, "docker", append([]string{"stop"}, ids...)...)
	if err != nil {
		return fmt.Errorf("docker stop: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}
