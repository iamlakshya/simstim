//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

// debug bundle writes one private archive in the current folder, of the
// laptop, a host or a session, says what it holds, and says that nothing
// was uploaded. What it replaces depends on the machine (a CI runner's user
// name is a generic word), so internal/bundle tests the replacements.
func TestTheDebugBundle(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "dbg", "/bin/sh", "-c", "exec sleep 60")
	ok(t, "debug", "start", "box/dbg", "--for", "1m", "--only", "memory")
	dir := t.TempDir()
	t.Chdir(dir)
	out := okSaid(t, "debug", "bundle", "box/dbg", "--ssh")
	for _, want := range []string{"Wrote ", ".tar.gz (", "README.txt", "replaced.txt", "host/doctor.json",
		"host/sessions/" + id + "/debug.jsonl", "host/sessions/" + id + "/debug/heap-", "In the archive, it replaced ",
		"Not gathered: no ssh -v log: box is reached through its provider, not SSH.",
		"Nothing was uploaded. Read the files before you attach them to an issue.\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("debug bundle box/dbg lacks %q:\n%s", want, out)
		}
	}
	out = okSaid(t, "debug", "bundle")
	if !strings.Contains(out, "laptop/doctor.txt") || strings.Contains(out, "host/") {
		t.Errorf("debug bundle of the laptop:\n%s", out)
	}
	bundles, _ := filepath.Glob(filepath.Join(dir, "*-debug-*.tar.gz"))
	if len(bundles) != 2 {
		t.Fatalf("bundles in the current folder: %v", bundles)
	}
	for _, b := range bundles {
		if st, err := os.Stat(b); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", b, st.Mode(), err)
		}
	}
	fails(t, "--ssh needs a host", "debug", "bundle", "--ssh")
	fails(t, `"box/" is not a host or host/session`, "debug", "bundle", "box/")
	fails(t, `no session "nosuch" on box`, "debug", "bundle", "box/nosuch")
}

// With a helper from before the debug mode, the bundle says that it is too
// old, and how to update it.
func TestTheBundleWithAnOldHelper(t *testing.T) {
	newTestHost(t)
	old := host.Features
	host.Features = slices.DeleteFunc(slices.Clone(old), func(f string) bool { return f == api.FeatureDebug })
	t.Cleanup(func() { host.Features = old })
	t.Chdir(t.TempDir())
	failsWithHint(t, "the host helper on box is too old for the debug mode", "host add box", "debug", "bundle", "box")
}
