//go:build linux

package slotlaunch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Run the actual undo section in a private ordinary-user filesystem. Every
// absolute system prefix is redirected, and no account or device exists in it.
// This proves caller-scoped metadata cleanup, not native root undo readiness.
func TestSlotsUndoRemovesOnlyTheCallersAdvisoryProjection(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "var/lib", brand.Name)
	machine := filepath.Join(store, "root-steps/machine/slots")
	for _, name := range []string{filepath.Join(machine, "users"), filepath.Join(machine, "capacity"), filepath.Join(store, "store")} {
		if err := os.MkdirAll(name, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{
		filepath.Join(machine, "users/caller"):       "1000\n",
		filepath.Join(machine, "users/another"):      "1001\n",
		filepath.Join(machine, "capacity/1000.json"): "caller observation",
		filepath.Join(machine, "capacity/1001.json"): "another observation",
		filepath.Join(store, "store/.lock"):          "",
	} {
		if err := os.WriteFile(name, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("../client/rootstep/undo-slots.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer("@BRAND@", brand.Name, "@USER@", "caller", "@UID@", "1000").Replace(string(raw))
	// The source section quotes these fixed paths. Escape any quote in the
	// private fixture prefix before inserting it into those same literals.
	prefix := strings.ReplaceAll(root, "'", "'\\''")
	script = strings.NewReplacer("/var/lib/", prefix+"/var/lib/", "/etc/", prefix+"/etc/", "/dev/", prefix+"/dev/").Replace(script)
	preamble := "set -eu\nbase=\"$CAPACITY_TEST_STORE/root-steps\"\nu=caller\n" +
		"safe() { [ ! -L \"$1\" ] && [ -e \"$1\" ]; }\n" +
		"fail() { printf '%s\\n' \"$*\" >&2; exit 1; }\n"
	command := exec.Command("/bin/sh", "-c", preamble+script)
	command.Env = append(os.Environ(), "CAPACITY_TEST_STORE="+store)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("private ordinary-user undo fixture failed: %v: %s", err, output)
	}
	for _, name := range []string{"capacity/1000.json", "users/caller"} {
		if _, err := os.Lstat(filepath.Join(machine, name)); !os.IsNotExist(err) {
			t.Fatal("undo retained the caller's projection or opt-in reference")
		}
	}
	for name, expected := range map[string]string{"capacity/1001.json": "another observation", "users/another": "1001\n"} {
		actual, err := os.ReadFile(filepath.Join(machine, name))
		if err != nil || string(actual) != expected {
			t.Fatal("undo changed another caller's metadata")
		}
	}
}
