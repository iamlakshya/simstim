//go:build linux

package main

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func init() {
	commands["operation-guest-v1"] = func(arguments []string) error {
		if len(arguments) != 1 || !api.ValidSessionID(arguments[0]) {
			return host.UserError("bad_request", "the auxiliary helper needs one exact primary ID", "Use the original stopped copy or pull operation.")
		}
		return host.RunAuxiliaryGuestV1(arguments[0])
	}
	host.SetStoppedGuestOpener(host.OpenAuxiliaryStoppedGuestV1)
}
