package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func writeSettings(t *testing.T, text string) {
	t.Helper()
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "config.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newSessionArgs starts a detached session with flags, and returns its ID.
func newSessionArgs(t *testing.T, flags ...string) string {
	t.Helper()
	args := append(append([]string{"new", "box", "--detach", "--wall", "none", "--downgrade"}, flags...), "--", "sleep", "60")
	m := startedRE.FindStringSubmatch(okSaid(t, args...))
	if m == nil {
		t.Fatalf("%q started nothing", args)
	}
	return m[1]
}
