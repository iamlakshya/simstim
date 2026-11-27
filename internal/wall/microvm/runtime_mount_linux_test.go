//go:build linux

package microvm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeReadOnlyOpenCannotReplaceReadOnlyMount(t *testing.T) {
	name := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(name, []byte("owned test bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := requireReadOnlyRuntimeMount(file); err == nil {
		t.Fatal("a read-only open on a writable mount acquired execution confinement")
	}
	// This is why open flags cannot supply the proof. No bytes leave the
	// owned fixture, but fchmod still works through this read-only handle.
	if err := file.Chmod(0o400); err != nil {
		t.Fatal("the writable-mount counterexample did not execute", err)
	}
}
