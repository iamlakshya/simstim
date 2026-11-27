//go:build linux

package main

import (
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func init() {
	// The real helper must recognise its own jail stages before ordinary
	// command dispatch. Test helper registration cannot supply this boundary.
	jail.Init()
	commands["wall"] = runWallClosed
}

func runWallClosed(args []string) error {
	if len(args) != 1 || args[0] != "probe-child" {
		return host.UserError("wall_probe", "the internal wall command needs exactly probe-child", "Run the host doctor to check sandbox support.")
	}
	// This is the real mount/seccomp producer. It checks its actual private
	// user and mount namespaces before changing either of them.
	return process.ProbeChild()
}
