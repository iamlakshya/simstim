package host

import (
	"slices"
	"strings"
)

// vmCommand owns only the existing runtime placeholders. Command names stay
// inside the guest's fixed PATH, which begins at the original agents image.
// No host login executable or host-home path enters this command plan.
func vmCommand(argv []string) []string {
	out := slices.Clone(argv)
	for i, value := range out {
		if value == "$SHELL" {
			out[i] = "/bin/sh"
		}
	}
	if len(out) > 0 && strings.HasPrefix(out[0], "~/") {
		out[0] = GuestHome + strings.TrimPrefix(out[0], "~")
	}
	return out
}
