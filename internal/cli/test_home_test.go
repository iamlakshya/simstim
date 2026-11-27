package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestHome gives a file-only test empty settings and private search paths.
// It starts no host helper, shell or session.
func newTestHome(t *testing.T) {
	t.Helper()
	isolateCompletionEnv(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
}

func TestFileOnlyFixtureIsolatesEveryHomeAndSearchPath(t *testing.T) {
	newTestHome(t)
	home := os.Getenv("HOME")
	if home == "" || os.Getenv("USERPROFILE") != home {
		t.Fatalf("HOME and USERPROFILE must name the same private home")
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		path := os.Getenv(name)
		rel, err := filepath.Rel(home, path)
		if path == "" || err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("%s = %q, outside the test home %q", name, path, home)
		}
	}
}
