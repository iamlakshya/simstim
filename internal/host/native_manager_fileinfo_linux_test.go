//go:build linux

package host

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeManagerSocketAcceptsActualOSFileInfoAndRefusesOtherTypes(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, "manager.sock")
	anchor, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	bind := fmt.Sprintf("/proc/self/fd/%d/manager.sock", anchor.Fd())
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: bind, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if !nativeManagerSocketOwnerV1(info) {
		t.Fatal("the same user's actual Unix socket was refused by native stat representation")
	}
	ordinary := filepath.Join(directory, "ordinary")
	if err := os.WriteFile(ordinary, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Lstat(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if nativeManagerSocketOwnerV1(regular) || nativeManagerSocketOwnerV1(nil) {
		t.Fatal("a non-socket supplied a native manager endpoint")
	}
	link := filepath.Join(directory, "linked.sock")
	if err := os.Symlink(name, link); err != nil {
		t.Fatal(err)
	}
	linked, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if nativeManagerSocketOwnerV1(linked) {
		t.Fatal("a symlink supplied the original native manager socket")
	}
}

func TestNativeManagerExecutableRequiresActualRootOwnedSameInode(t *testing.T) {
	var trusted os.FileInfo
	for _, name := range []string{"/bin/sh", "/bin/true", "/usr/bin/env"} {
		info, err := os.Stat(name)
		if err == nil && info.Mode().IsRegular() {
			trusted = info
			break
		}
	}
	if trusted == nil {
		t.Fatal("the native Linux fixture has no standard root-owned executable")
	}
	if !nativeManagerExecutableOwnerV1(trusted, trusted) {
		t.Fatal("actual root-owned executable FileInfo was refused by native stat representation")
	}
	name := filepath.Join(t.TempDir(), "another-executable")
	if err := os.WriteFile(name, []byte("different native inode"), 0o700); err != nil {
		t.Fatal(err)
	}
	other, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if nativeManagerExecutableOwnerV1(other, trusted) || nativeManagerExecutableOwnerV1(trusted, other) || nativeManagerExecutableOwnerV1(nil, trusted) {
		t.Fatal("another inode or absent executable replaced native manager code")
	}
	if err := os.Chmod(name, 0o722); err != nil {
		t.Fatal(err)
	}
	writable, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if nativeManagerExecutableOwnerV1(writable, writable) {
		t.Fatal("a writable executable became native manager code")
	}
}
