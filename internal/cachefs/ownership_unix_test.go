//go:build unix

package cachefs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCacheFilesRefuseForeignOwnerWithoutChmodOrCleanup(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root in the isolated ownership job")
	}
	root, dir := privateRoot(t)
	writeChecked(t, root, "foreign", []byte("keep"))
	path := filepath.Join(dir, "foreign")
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenFile(root, "foreign", os.O_RDWR|os.O_CREATE, 0o600); err == nil {
		file.Close()
		t.Fatal("foreign owner accepted")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 65534 || info.Mode().Perm() != 0o600 {
		t.Fatal("foreign ownership or mode changed")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatalf("foreign bytes changed: %q %v", data, err)
	}
}

func TestPrivateChildRefusesUnsafeModeWithoutChmod(t *testing.T) {
	root, dir := privateRoot(t)
	if err := root.Mkdir("child", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "child"), 0o777); err != nil {
		t.Fatal(err)
	}
	if child, err := OpenPrivateChild(root, "child", true); err == nil {
		child.Close()
		t.Fatal("unsafe child accepted")
	}
	info, err := os.Lstat(filepath.Join(dir, "child"))
	if err != nil || info.Mode().Perm() != 0o777 {
		t.Fatalf("unsafe child chmodded: %v", err)
	}
}

func TestPrivateRootRefusesSymlinkAncestorBeforeCreation(t *testing.T) {
	base := isolatedRoot(t)
	target := filepath.Join(base, "foreign")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if root, err := OpenPrivateRoot(filepath.Join(link, "cache"), true); err == nil {
		root.Close()
		t.Fatal("symlink ancestor accepted")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("generic root wrote through foreign ancestor: %v %v", entries, err)
	}
}
