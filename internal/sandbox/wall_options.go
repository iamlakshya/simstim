package sandbox

import "github.com/iamlakshya/simstim/internal/proto"

// ValidateVMOptions checks resolved applied options before any guest starts.
// Config parsing alone cannot decide this: auto first resolves the actual wall.
func ValidateVMOptions(level string, docker, sudo bool) error {
	if level == "microvm" {
		return nil
	}
	if docker {
		return &proto.Error{Code: "docker_wall", Message: "Docker needs a microvm sandbox", Hint: "Choose --wall microvm and an image with dockerd, or disable sandbox.docker."}
	}
	if sudo {
		return &proto.Error{Code: "sudo_wall", Message: "sudo needs a microvm sandbox", Hint: "Choose --wall microvm, or disable sandbox.sudo."}
	}
	return nil
}
