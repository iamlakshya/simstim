package hosttest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

// A test host's folders are its own, the sockets of shared SSH
// connections too, and a session's socket in its runtime folder fits in
// the 103 bytes that macOS allows. That holds also where the temporary
// folder is a short link to a long folder: the length of a test host's
// folder counts with its links resolved.
func TestATestHostsSocketFits(t *testing.T) {
	for _, linked := range []bool{false, true} {
		if linked {
			long := filepath.Join(t.TempDir(), strings.Repeat("x", 40))
			links, err := os.MkdirTemp("/tmp", "l")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(links) })
			link := filepath.Join(links, "t")
			if err := os.MkdirAll(long, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(long, link); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", link)
		}
		h := New(t)
		if resolved, err := filepath.EvalSymlinks(h.Root); err != nil || resolved != h.Root {
			t.Errorf("the test host's folder %s is not as its links resolve it: %s, %v", h.Root, resolved, err)
		}
		for _, d := range []string{paths.StateDir(), paths.ConfigDir(), paths.RuntimeDir(), client.ControlDir()} {
			if !strings.HasPrefix(d, h.Root+"/") {
				t.Errorf("linked %v: %s is not in the test host's folder %s", linked, d, h.Root)
			}
		}
		if s := paths.Socket(host.NewID()); len(s) > 103 {
			t.Errorf("linked %v: a session's socket %s has %d bytes, more than 103", linked, s, len(s))
		}
	}
}
