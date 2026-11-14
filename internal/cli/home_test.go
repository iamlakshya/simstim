package cli

import (
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The tests run in a home of their own: no test reads or writes the
// user's real home, state folder or debug folder.
func TestTheTestsHaveAHomeOfTheirOwn(t *testing.T) {
	dirs := []string{client.StateDir(), client.ConfigDir(), client.CacheDir(), paths.RuntimeDir()}
	if d := client.ControlDir(); d != "" || runtime.GOOS != "windows" {
		dirs = append(dirs, d) // the sockets of shared SSH connections
	}
	testhome.Check(t, dirs...)
}
