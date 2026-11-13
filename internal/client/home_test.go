package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The tests run in a home of their own: no test reads or writes the
// user's real home, state folder or debug folder, nor uses the shared
// runtime folder. Where the client shares SSH connections, their sockets
// have room in the cache folder, so they do not fall back to the shared
// folder in /tmp. On Windows it shares none.
func TestTheTestsHaveAHomeOfTheirOwn(t *testing.T) {
	dirs := []string{StateDir(), ConfigDir(), CacheDir(), paths.RuntimeDir()}
	if controlMaster {
		dirs = append(dirs, ControlDir())
	}
	testhome.Check(t, dirs...)
}
