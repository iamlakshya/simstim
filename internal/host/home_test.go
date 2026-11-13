package host

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The tests run in a home of their own: no test reads or writes the
// user's real home, state folder or debug folder, nor uses the shared
// runtime folder. A session's socket there fits in the 103 bytes that
// macOS allows.
func TestTheTestsHaveAHomeOfTheirOwn(t *testing.T) {
	testhome.Check(t, client.StateDir(), paths.StateDir(), paths.ConfigDir(), paths.DataDir(), paths.RuntimeDir())
	if s := paths.Socket(NewID()); len(s) > 103 {
		t.Errorf("a session's socket %s has %d bytes, more than 103", s, len(s))
	}
}
