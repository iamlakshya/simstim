package main

import (
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() {
	commands["wall-sftp-v1"] = func(args []string) error {
		if len(args) != 0 {
			return &proto.Error{Code: "bad_request", Message: "the guest SFTP helper takes no arguments", Hint: "Start SFTP through the generated SSH configuration."}
		}
		return guest.RunEditorSFTPV1()
	}
}
