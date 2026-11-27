package gvisor

import "github.com/iamlakshya/simstim/internal/proto"

func failure(code, message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	hint := "Run host prepare and host doctor, then resume the session."
	if code == "runtime_advisory" {
		hint = "Upgrade the host runtime from this client's current release before starting again."
	}
	return &proto.Error{Code: code, Message: message, Hint: hint}
}
