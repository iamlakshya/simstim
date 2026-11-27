package helpers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestHelperArchiveUsesCanonicalLeaves(t *testing.T) {
	var bundle bytes.Buffer
	zip := gzip.NewWriter(&bundle)
	archive := tar.NewWriter(zip)
	add := func(name, body string, kind byte) {
		t.Helper()
		h := &tar.Header{Name: name, Mode: 0o600, Typeflag: kind, Size: int64(len(body))}
		if kind == tar.TypeSymlink {
			h.Linkname, h.Size = "../outside", 0
		}
		if err := archive.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := archive.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("host-linux-amd64", "host", tar.TypeReg)
	add("nested/vt-linux-arm64", "terminal", tar.TypeReg)
	for _, name := range []string{"../host-linux-amd64", "/host-linux-amd64", `host-linux-amd64\..\outside`, `nested\host-linux-amd64`, "host-rogue-arch", "vt-linux-amd64:stream"} {
		add(name, "unwanted", tar.TypeReg)
	}
	add("host-linux-arm64", "", tar.TypeSymlink)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := untar(bundle.Bytes())
	if err != nil || len(got) != 2 || string(got["host-linux-amd64"]) != "host" || string(got["vt-linux-arm64"]) != "terminal" {
		t.Fatalf("archive files = %q, %v, want only the two canonical helpers", got, err)
	}
}

func TestHelperCacheRejectsUnrecognisedLeaves(t *testing.T) {
	for _, name := range []string{"../outside", `host-linux-amd64\..\outside`, "host-rogue-arch", "vt-linux-amd64:stream"} {
		dir := t.TempDir()
		if err := cacheHelpers(dir, map[string][]byte{name: []byte("unwanted")}); err == nil {
			t.Fatalf("cache accepted %q", name)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("invalid name %q wrote cache files: %v, %v", name, entries, err)
		}
	}
}

func TestHelperCacheCannotFollowAnOutsideLink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "host-linux-amd64")); err != nil {
		t.Skipf("filesystem cannot make a symbolic link: %v", err)
	}
	if err := cacheHelpers(dir, map[string][]byte{"host-linux-amd64": []byte("unwanted")}); err == nil {
		t.Fatal("cache write followed a link outside its root")
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "kept" {
		t.Fatalf("outside file = %q, %v, want kept", got, err)
	}
}
