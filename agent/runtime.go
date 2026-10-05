package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"snivur/v0/shared"
)

// Runtime starts game server containers.
type Runtime interface {
	Start(ctx context.Context, req shared.LaunchRequest) (containerID string, err error)
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
