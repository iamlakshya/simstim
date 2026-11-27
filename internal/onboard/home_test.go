package onboard

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The tests run in a home of their own (testhome).
func TestMain(m *testing.M) { testhome.Main(m) }

// The tests run in a home of their own: no test reads or writes the
// user's real home, state folder or debug folder.
func TestTheTestsHaveAHomeOfTheirOwn(t *testing.T) {
	testhome.Check(t, client.StateDir(), client.ConfigDir(), client.CacheDir(), paths.RuntimeDir())
}
