//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// new -d <folder> starts the session in that folder, as it did, and warns.
func TestNewWithTheOldDashD(t *testing.T) {
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")
	dir := filepath.Join(th.h.Home, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := mainLine(t, "new", "box", "-d", dir, "--detach", "--wall", "none", "--downgrade", "--name", "old-d", "--", "sleep", "60")
	m := startedRE.FindStringSubmatch(stderr)
	if code != 0 || m == nil || !strings.HasPrefix(stderr, warningOf(t, "new -d", "", "")) || strings.Count(stderr, "Warning:") != 1 {
		t.Fatalf("new -d = %d, %q:\n%s", code, out, stderr)
	}
	if _, stderr, code := mainLine(t, "new", "box", "--setup", "--no-setup", "--detach"); code != 2 || !strings.Contains(stderr, "Error: --setup-script and --no-setup-script cannot go together.") {
		t.Errorf("new box --setup --no-setup = %d:\n%s", code, stderr)
	}
	var list []hostSessions
	if err := json.Unmarshal([]byte(ok(t, "ls", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, r := range list {
		for _, s := range r.Sessions {
			if s.ID == m[1] {
				found = append(found, s.Cwd)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("ls shows session %s %d times: %+v", m[1], len(found), list)
	}
	got, err := filepath.EvalSymlinks(found[0])
	if err != nil || got != want {
		t.Errorf("the session runs in %q (%v), want %q", found[0], err, want)
	}
}
