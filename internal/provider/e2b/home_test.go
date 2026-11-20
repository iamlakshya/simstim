package e2b

import (
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The tests run in a home of their own (testhome).
func TestMain(m *testing.M) {
	if os.Getenv(wrapperEnv) != "" {
		// The fake sets atexit_sleep_ms=0 in the wrapper's GORACE, so the
		// race detector reports a race, with exit status 66, but does not
		// sleep a second at exit. Keep its inherited home and descriptors.
		os.Exit(runWrapper(os.Args[1:]))
	}
	testhome.Main(m)
}

// The tests run in a home of their own: no test reads or writes the
// user's real home and folders, such as the E2B login, nor uses the shared runtime folder.
func TestTheTestsHaveAHomeOfTheirOwn(t *testing.T) {
	testhome.Check(t, client.StateDir(), client.ConfigDir(), client.CacheDir(), paths.RuntimeDir(), cliConfigPath())
}
