//go:build linux

package slotlaunch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are actual temporary filesystem publication checks. They invoke no
// formatter, account change, cgroup or VMM and are not guest-boot evidence.
func TestFreshDiskRemainsPrivateUntilNoReplacePublication(t *testing.T) {
	b, dir := testBackend(t)
	request := validStart()
	request.DiskMaxGiB = 1
	folder, disk, fresh, err := b.prepareVM(request, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	defer disk.Close()
	if !fresh || !strings.Contains(disk.Name(), "/.new-disk.") {
		t.Fatal("fresh disk was not a private temporary capture")
	}
	canonical := filepath.Join(dir, "vms", request.Session, "disk.img")
	if _, err := os.Stat(canonical); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an unformatted fresh disk became canonical", err)
	}
	if err := b.publishFreshDisk(request.Session, disk); err != nil {
		t.Fatal(err)
	}
	actual, err := os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	original, err := disk.Stat()
	if err != nil || !os.SameFile(actual, original) {
		t.Fatal("publication changed the captured disk identity")
	}
	if err := b.removeCapturedFile(disk.Name(), disk); err != nil {
		t.Fatal(err)
	}
}

func TestFreshDiskCannotReplaceAnExistingDiskOrDeleteAReplacement(t *testing.T) {
	b, dir := testBackend(t)
	request := validStart()
	request.DiskMaxGiB = 1
	folder, disk, _, err := b.prepareVM(request, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	defer disk.Close()
	canonical := filepath.Join(dir, "vms", request.Session, "disk.img")
	old := []byte("existing disk remains")
	if err := os.WriteFile(canonical, old, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := b.publishFreshDisk(request.Session, disk); err == nil {
		t.Fatal("fresh disk replaced an existing disk")
	}
	actual, err := os.ReadFile(canonical)
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("existing disk changed", err)
	}
	temporary := filepath.Join(dir, disk.Name())
	if err := os.Remove(temporary); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("replacement retained"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := b.removeCapturedFile(disk.Name(), disk); err == nil {
		t.Fatal("cleanup accepted a replacement inode")
	}
	actual, err = os.ReadFile(temporary)
	if err != nil || string(actual) != "replacement retained" {
		t.Fatal("cleanup deleted the replacement", err)
	}
}
