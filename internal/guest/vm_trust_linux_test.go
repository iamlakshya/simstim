//go:build linux

package guest

import (
	"github.com/iamlakshya/simstim/internal/brand"
	"os"
	"path/filepath"
	"testing"
)

func TestVMPublicTrustIsReadableBy1000WithoutChangingOrdinaryMode(t *testing.T) {
	for _, vm := range []bool{false, true} {
		root := t.TempDir()
		relative := filepath.Join("run", brand.Name)
		if err := os.MkdirAll(filepath.Join(root, relative), 0o755); err != nil {
			t.Fatal(err)
		}
		guest := &Init{root: root}
		if vm {
			guest.vm = &vmRuntime{}
		}
		name := "/" + filepath.ToSlash(filepath.Join(relative, "ca.pem"))
		if err := guest.writeGuestTrust(name, []byte("PUBLIC CERTIFICATE ONLY")); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, relative, "ca.pem"))
		if err != nil {
			t.Fatal(err)
		}
		expected := os.FileMode(0o600)
		if vm {
			expected = 0o644
		}
		if info.Mode().Perm() != expected {
			t.Fatal("public VM trust unreadable or ordinary mode changed", vm, info.Mode())
		}
	}
}
