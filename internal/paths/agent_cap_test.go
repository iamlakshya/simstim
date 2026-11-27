//go:build !windows

package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPrivateRuntimeDirPinsItsDirectory(t *testing.T) {
	xdg := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	r, err := OpenPrivateRuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	name := r.Name()
	moved := name + "-moved"
	if err := os.Rename(name, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(name, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteFile("owned", []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(moved, "owned")); err != nil || string(b) != "original" {
		t.Fatalf("capability lost the original directory: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(name, "owned")); !os.IsNotExist(err) {
		t.Fatalf("capability wrote to the replacement: %v", err)
	}
}
