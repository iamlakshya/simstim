//go:build linux

package supply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGvisorCompanionsUseSameImmutableByteValidator(t *testing.T) {
	for _, name := range []string{"runsc", "gvisor_sentry", "gvisor-sentry-prewarmer", "gvisor-proc-umounter", "runsc-fd-parking"} {
		t.Run(name, func(t *testing.T) {
			// These are metadata fixtures. No executable is started here.
			data := []byte("owned runtime fixture: " + name)
			item := NewItem(KindGvisor, name, "release-20261102.0", runtime.GOARCH, data, ReleaseFolder("1.0.0")+"/"+name)
			item.Released = "2026-11-02"
			path := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(path, data, 0o555); err != nil {
				t.Fatal(err)
			}
			source, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			sealed, err := SealRuntime(context.Background(), item, source)
			if err != nil {
				t.Fatal(err)
			}
			defer sealed.Close()
			if err := CheckSealedRuntime(item, sealed); err != nil {
				t.Fatal(err)
			}
			if _, err := sealed.WriteAt([]byte("changed"), 0); !errors.Is(err, unix.EPERM) {
				t.Fatal("a sealed companion allowed byte mutation", err)
			}
			info, err := sealed.Stat()
			if err != nil || info.Mode().Perm() != 0o555 {
				t.Fatal("a companion lost its readable executable mode", err)
			}
			altered := item
			altered.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if err := CheckSealedRuntime(altered, sealed); err == nil {
				t.Fatal("a sealed companion bypassed its original byte digest")
			}
			if err := sealed.Chmod(0o111); err != nil {
				t.Fatal(err)
			}
			if err := CheckSealedRuntime(item, sealed); err == nil {
				t.Fatal("a companion acquired a different execution mode")
			}
		})
	}
}
