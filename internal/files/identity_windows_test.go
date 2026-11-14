//go:build windows

package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsOpenedFileIdentityPreservesHardlinksAndDistinctObjects(t *testing.T) {
	directory := t.TempDir()
	first, alias, second := filepath.Join(directory, "first"), filepath.Join(directory, "alias"), filepath.Join(directory, "second")
	for _, name := range []string{first, second} {
		if err := os.WriteFile(name, []byte("identity fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(first, alias); err != nil {
		t.Fatal(err)
	}
	var identities []string
	for _, name := range []string{first, alias, second} {
		file, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := Identity(file)
		file.Close()
		if err != nil || identity == "" {
			t.Fatalf("native identity failed: %v", err)
		}
		identities = append(identities, identity)
	}
	if identities[0] != identities[1] || identities[0] == identities[2] {
		t.Fatalf("native hardlink/volume identity changed: %q", identities)
	}
	closed, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if identity, err := Identity(closed); err == nil || identity != "" {
		t.Fatal("closed-handle failure became a successful invented identity")
	}
}

func TestWindowsIdentityUsesVolumeAndAll128FileIDBits(t *testing.T) {
	first := windowsFileIDInfo{Volume: 1, ID: [16]byte{1}}
	high := first
	high.ID[15] = 1
	volume := first
	volume.Volume++
	if windowsFileIdentity(first) == windowsFileIdentity(high) || windowsFileIdentity(first) == windowsFileIdentity(volume) {
		t.Fatal("native volume or high file-ID bits were truncated")
	}
}
