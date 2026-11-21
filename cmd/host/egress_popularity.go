package main

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

func runPopularityEgressV1(args []string) (bool, error) {
	if len(args) != 2 {
		return false, nil
	}
	base, known := egress.ParsePopularityFDArgumentV1(args[1])
	if !known {
		return false, nil
	}
	if !api.ValidSessionID(args[0]) {
		return true, host.UserError("bad_request", "egress needs one valid session ID", "Let the session supervisor start its egress child.")
	}
	return true, egress.RunPopularityV1(args[0], base)
}
