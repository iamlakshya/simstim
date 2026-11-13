package cli

import (
	"strings"
	"testing"
)

// The help keeps the safety checks already present when the teaching
// layout is added. Plain wording must not hide those checks.
func TestHelpKeepsHostAndForwardingChecks(t *testing.T) {
	for _, tc := range []struct {
		command string
		text    string
		checks  []string
	}{
		{"host doctor", hostDoctorCmd().Long, []string{
			"sessions keep running after you log out",
			"brings sessions back after a reboot",
			"drive another through its socket",
			"cannot reach services on this computer's loopback address",
		}},
		{"forward", forwardCmd().Long, []string{
			"SSH connection of its own",
			"RemoteForward", "Tunnel", "ForkAfterAuthentication yes",
		}},
	} {
		text := strings.Join(strings.Fields(tc.text), " ")
		for _, check := range tc.checks {
			if !strings.Contains(text, check) {
				t.Errorf("%s help lost %q: %s", tc.command, check, text)
			}
		}
	}
}
