package runtimecache

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestRuntimeManifestRolesArchitectureAndOverflow(t *testing.T) {
	base := testManifest([]byte("file"), "r1")
	if err := Validate(base, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*supply.Manifest)
	}{
		{"wrong architecture", func(m *supply.Manifest) {
			m.Items[0].Arch = "amd64"
			if runtime.GOARCH == "amd64" {
				m.Items[0].Arch = "arm64"
			}
		}},
		{"helper role", func(m *supply.Manifest) { m.Items[0].Kind = supply.KindHelper }},
		{"image role", func(m *supply.Manifest) { m.Items[0].Kind = supply.KindImage }},
		{"privileged launcher", func(m *supply.Manifest) { m.Items[0].Kind = supply.KindLaunch }},
		{"overflow", func(m *supply.Manifest) { m.Items[0].Size = math.MaxInt64 }},
		{"outside release", func(m *supply.Manifest) { m.Items[0].URL = "https://fixture.invalid/file" }},
		{"path label", func(m *supply.Manifest) { m.Runtime = "../outside" }},
		{"empty items", func(m *supply.Manifest) { m.Items = nil }},
		{"conflicting modes", func(m *supply.Manifest) {
			it := m.Items[0]
			it.Kind = supply.KindGvisor
			it.Name = "runsc"
			it.Released = "2026-01-01"
			m.Items = append(m.Items, it)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := base
			m.Items = append([]supply.Item(nil), base.Items...)
			test.change(&m)
			if err := Validate(m, runtime.GOARCH); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
	// A valid signed size above4GiB and more than64 distinct entries remain
	// accepted. Disk admission, not a new feature cap, decides delivery.
	large := base
	large.Items = append([]supply.Item(nil), base.Items...)
	large.Items[0].Size = 5 << 30
	for i := 0; i < 65; i++ {
		it := large.Items[0]
		it.Name = string(rune('A' + i))
		large.Items = append(large.Items, it)
	}
	if err := Validate(large, runtime.GOARCH); err != nil {
		t.Fatalf("arbitrary lower cap: %v", err)
	}
}

func TestStoredCorruptOwnedObjectCanBeRepaired(t *testing.T) {
	store, dir := testStore(t)
	body := []byte("good")
	item := testManifest(body, "r1").Items[0]
	target := filepath.Join(dir, "sha256", item.SHA256)
	if err := os.WriteFile(target, []byte("evil"), 0o444); err != nil {
		t.Fatal(err)
	}
	if has, err := store.Has(context.Background(), item); err != nil || has {
		t.Fatalf("corrupt object is not missing: %t %v", has, err)
	}
	installBody(t, store, item, body)
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("corrupt object not repaired: %q %v", got, err)
	}
	assertNoParts(t, dir)
}

func TestStoredSymlinkAndHardlinkRefusedWithoutForeignCleanup(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			store, dir := testStore(t)
			body := []byte("data")
			item := testManifest(body, "r1").Items[0]
			foreign := filepath.Join(filepath.Dir(dir), "foreign")
			if err := os.WriteFile(foreign, body, 0o444); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "sha256", item.SHA256)
			var err error
			if kind == "symlink" {
				err = os.Symlink(foreign, target)
			} else {
				err = os.Link(foreign, target)
			}
			if err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("link creation unavailable: %v", err)
				}
				t.Fatal(err)
			}
			p, err := store.Begin(item)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.WriteAt(body, 0); err != nil {
				t.Fatal(err)
			}
			if err := p.Finish(context.Background()); err == nil {
				t.Fatal("foreign link accepted")
			}
			got, err := os.ReadFile(foreign)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("foreign target changed: %q %v", got, err)
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatalf("foreign link removed: %v", err)
			}
			assertNoParts(t, dir)
		})
	}
}

func TestReadOnlyOpenNeverCreatesMissingCache(t *testing.T) {
	dir := filepath.Join(cacheTestRoot(t), "absent", "cache")
	if _, err := OpenReadOnly(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only missing cache: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only lookup created a folder: %v", err)
	}
}

func TestStoreRefusesSymlinkAncestorWithoutWritingThroughIt(t *testing.T) {
	root := cacheTestRoot(t)
	foreign := filepath.Join(root, "foreign")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(foreign, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(link, "store")); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	if entries, err := os.ReadDir(foreign); err != nil || len(entries) != 0 {
		t.Fatalf("wrote through foreign ancestor: %v %v", entries, err)
	}
}

func TestRuntimeModesFollowAuthorisedRoles(t *testing.T) {
	cacheTestRoot(t)
	for _, test := range []struct {
		kind string
		want os.FileMode
	}{
		{supply.KindUserns, 0o444}, {supply.KindKernel, 0o444}, {supply.KindTools, 0o444}, {supply.KindVMM, 0o555}, {supply.KindGvisor, 0o555},
	} {
		if got := Mode(supply.Item{Kind: test.kind}); got != test.want {
			t.Fatalf("role %s has mode %04o, want %04o", test.kind, got, test.want)
		}
	}
}
