package alerts

import (
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The tests run in a home of their own (testhome): HOME moves too, not
// only the state and config folders.
func TestMain(m *testing.M) { testhome.Main(m) }

// The tests run in a home of their own: no test reads or writes the
// user's real home, alerts file or alerts log, nor uses the shared runtime
// folder. A hook runs in the home folder.
func TestTheTestsHaveAHomeOfTheirOwn(t *testing.T) {
	home, _ := os.UserHomeDir()
	testhome.Check(t, paths.AlertsConfig(), paths.AlertsLog(), paths.RuntimeDir(), home)
}
