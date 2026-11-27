package main

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

func init() { commands["egress"] = runEgress }

// The supervisor launches this internal child with inherited private FDs.
func runEgress(args []string) error {
	if len(args) != 1 || !api.ValidSessionID(args[0]) {
		return host.UserError("bad_request", "egress needs one valid session ID", "Let the session supervisor start its egress child.")
	}
	return egress.Run(args[0])
}
