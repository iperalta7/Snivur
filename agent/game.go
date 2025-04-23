package main

import (
	"os/exec"
	"snivur/v0/shared"
)

type GameServer interface {
	Run(req shared.LaunchRequest) *exec.Cmd
}
