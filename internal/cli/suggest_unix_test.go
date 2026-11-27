//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// A hosts.toml that does not decode is the user's to fix: it says where,
// and that config check finds each mistake.
// One that cannot be read is a failure.
func TestUnreadableHostsFile(t *testing.T) {
	newTestHome(t)
	path := filepath.Join(client.ConfigDir(), "hosts.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[[host]]
name = "box"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "host", "ls"); code != 1 || !strings.Contains(got, "permission denied") || !strings.Contains(got, "debug bundle") {
		t.Errorf("an unreadable hosts.toml gives %d:\n%s", code, got)
	}
}

// A host's codes exit with the kind of the 5.1 table, through Main, the
// same as the client's own checks of the same mistakes.
func TestHostCodesExitThroughMain(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "s", "sleep", "60")
	old := os.Args
	defer func() { os.Args = old }()
	for _, c := range []struct {
		args []string
		code int
		has  string
	}{
		{[]string{"new", "box", "--agent", "nosuch", "--detach", "--wall", "none", "--downgrade"}, 2, "no canonical local command"},
		{[]string{"rename", "box/" + id, " "}, 2, "a name is needed"},
		{[]string{"new", "box", "--memory-max", "1M", "--detach"}, 2, "--memory-max"},
		{[]string{"new", "box", "--dir", "/no/such/folder", "--detach", "--wall", "none", "--downgrade"}, 4, "does not exist"},
		{[]string{"host", "rm", "nowhere"}, 3, "no host named"},
		{[]string{"atach"}, 2, "unknown command"},
		{[]string{"config", "path"}, 0, ""},
	} {
		os.Args = append([]string{"client"}, c.args...)
		got, code := captureStderr(t, Main)
		if code != c.code || !strings.Contains(got, c.has) {
			t.Errorf("Main %q = %d, want %d:\n%s", c.args, code, c.code, got)
		}
	}
}

// A hint names the session that the command found by host/ID, not by the
// words that the user typed, which a shell would need quoted.
func TestHintsQuoteWhatTheUserTyped(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "a b", "sleep", "60")
	got, code := lineErr(t, "forward", "box/a b", "x;y")
	if want := "\nTry: " + brand.Name + " forward box/" + id + " 3000\n"; code != 2 || !strings.Contains(got, want) {
		t.Errorf("forward = %d:\n%s\nwant %q", code, got, want)
	}
}
