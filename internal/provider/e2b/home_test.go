package e2b

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The tests run in a home of their own: no test reads or writes the
// user's real home and folders, such as the E2B login, nor uses the shared runtime folder.
func TestTheTestsHaveAHomeOfTheirOwn(t *testing.T) {
	testhome.Check(t, client.StateDir(), client.ConfigDir(), client.CacheDir(), paths.RuntimeDir(), cliConfigPath())
}
