package image

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func savedPinFixture(host, ref string) Pin {
	digest := "sha256:" + strings.Repeat("a", 64)
	return Pin{Ref: ref, HostID: host, Digest: digest, PinnedAt: time.Unix(1, 0),
		Platforms: []PlatformPin{{Arch: "amd64", Digest: digest, RootHash: strings.Repeat("b", 64), DataBlocks: 1}}}
}

func TestSavedPinsKeepHostNamespacesAndSavedReferenceGrammar(t *testing.T) {
	first := savedPinFixture(strings.Repeat("1", 32), "a..")
	second := savedPinFixture(strings.Repeat("2", 32), "a..")
	lock := Lock{Version: 1, Images: []Pin{first, second}}
	if err := lock.validate(); err != nil {
		t.Fatalf("same saved name on two hosts: %v", err)
	}
	lock.Images = append(lock.Images, first)
	if err := lock.validate(); err == nil {
		t.Fatal("duplicate saved name on its original host accepted")
	}
	registry := first
	registry.HostID = ""
	if err := (&Lock{Version: 1, Images: []Pin{registry}}).validate(); err == nil {
		t.Fatal("saved-only reference widened registry grammar")
	}
	registry.Ref = "tools"
	if err := (&Lock{Version: 1, Images: []Pin{registry, registry}}).validate(); err == nil {
		t.Fatal("duplicate registry reference accepted")
	}
	if err := (&Lock{Version: 1, Images: []Pin{registry, savedPinFixture(first.HostID, "tools")}}).validate(); err != nil {
		t.Fatalf("registry and saved namespaces collided: %v", err)
	}
}

func TestSavedPinMetadataCannotInventRegistryAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Pin)
	}{
		{"invalid host", func(p *Pin) { p.HostID = "host-name" }},
		{"invalid saved name", func(p *Pin) { p.Ref = "registry.example/tools:tag" }},
		{"manifest signer", func(p *Pin) { p.SignedBy = "signer" }},
		{"manifest policy", func(p *Pin) { p.Policy = "verified" }},
		{"different platform digest", func(p *Pin) { p.Platforms[0].Digest = "sha256:" + strings.Repeat("c", 64) }},
		{"missing root", func(p *Pin) { p.Platforms[0].RootHash = ""; p.Platforms[0].DataBlocks = 0 }},
		{"platform signer", func(p *Pin) { p.Platforms[0].SignedBy = "signer" }},
		{"two architectures", func(p *Pin) { p.Platforms = append(p.Platforms, PlatformPin{Arch: "arm64", Digest: p.Digest}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pin := savedPinFixture(strings.Repeat("1", 32), "tools")
			test.mutate(&pin)
			if err := (&Lock{Version: 1, Images: []Pin{pin}}).validate(); err == nil {
				t.Fatal("invalid saved metadata accepted")
			}
		})
	}
}

func TestReadingClientPinsDoesNotCreateOrRewriteTheirDocument(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	lock, err := ReadLock(context.Background(), missing)
	if err != nil || lock.Version != 1 || len(lock.Images) != 0 {
		t.Fatalf("absent pin snapshot: %+v %v", lock, err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("reading absent pins created a folder", err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateLock(context.Background(), dir, func(lock *Lock) error {
		lock.Images = []Pin{savedPinFixture(strings.Repeat("1", 32), "a..")}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "images.lock")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lock, err = ReadLock(context.Background(), dir)
	if err != nil || len(lock.Images) != 1 || lock.Images[0].Ref != "a.." {
		t.Fatalf("saved pin snapshot: %+v %v", lock, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("reading pins rewrote the document", err)
	}
}
