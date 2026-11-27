//go:build linux

package nitrodev

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceIdentityRejectsOrdinaryAndOtherDevices(t *testing.T) {
	dev := unix.Mkdev(10, 258)
	if !Matches(unix.S_IFCHR|0600, dev, "10:258\n") {
		t.Fatal("actual native misc identity rejected")
	}
	for _, tc := range []struct {
		mode  uint32
		dev   uint64
		value string
	}{{unix.S_IFREG | 0600, dev, "10:258"}, {unix.S_IFCHR, unix.Mkdev(1, 3), "10:258"}, {unix.S_IFCHR, dev, "1:258"}, {unix.S_IFCHR, dev, "10:258:3"}} {
		if Matches(tc.mode, tc.dev, tc.value) {
			t.Fatal("foreign device acquired Nitro identity")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "device")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenAt(path, dir); err == nil || file != nil {
		t.Fatal("ordinary file advertised a usable native device")
	}
}

func TestCPUListsRejectOverlapAndUnknownBudget(t *testing.T) {
	if n, err := CPUCount("0-3,5,8-10\n"); err != nil || n != 8 {
		t.Fatalf("CPUs %d %v", n, err)
	}
	for _, input := range []string{"", "0-3,2", "0,0", "-1", "2-1", "0-999999", "0,x"} {
		if _, err := CPUCount(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
