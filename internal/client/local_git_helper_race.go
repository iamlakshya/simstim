//go:build race && (linux || darwin)

package client

import (
	"slices"
	"strings"
)

func localGitHelperEnv(env []string) []string {
	out := slices.Clone(env)
	for i, entry := range out {
		if value, ok := strings.CutPrefix(entry, "GORACE="); ok {
			if !strings.Contains(value, "atexit_sleep_ms=") {
				out[i] = "GORACE=" + strings.TrimSpace(value+" atexit_sleep_ms=0")
			}
			return out
		}
	}
	return append(out, "GORACE=atexit_sleep_ms=0")
}
