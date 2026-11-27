package main

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/supervisor"
)

func init() {
	commands["supervise"] = func(args []string) error {
		if len(args) != 1 || !api.ValidSessionID(args[0]) {
			return host.UserError("bad_request", "supervise needs one session ID", "Start it through the canonical session launcher.")
		}
		if err := host.InitPlacement(); err != nil {
			return err
		}
		return supervisor.Run(args[0])
	}
	commands["supervise-restore-v1"] = func(args []string) error {
		if len(args) != 1 || !api.ValidSessionID(args[0]) {
			return host.UserError("bad_request", "automatic supervise needs one session ID", "Use the canonical host restore operation.")
		}
		if err := host.InitPlacement(); err != nil {
			return err
		}
		return supervisor.RunAutomaticRestore(args[0])
	}
	host.RegisterPullSource(supervisor.OpenPullSource)
}
