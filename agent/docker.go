package main

import (
	"os/exec"
	"snivur/v0/shared"
)

type DockerServer struct{}

func (d DockerServer) Run(req shared.LaunchRequest) *exec.Cmd {
	args := []string{"run", "--rm", "--name", req.Name}

	docker_image := req.Config["image"]

	args = append(args, docker_image)
	return exec.Command("docker", args...)
}
