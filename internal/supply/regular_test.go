package supply

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func regularReaders(t *testing.T) (map[string]func(string) error, []byte) {
	t.Helper()
	data, err := os.ReadFile("../../guest/supply.lock")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	item := NewItem(KindHelper, "helper", "0.6.0", "amd64", data, base+"helper")
	return map[string]func(string) error{
		"lock": func(p string) error { _, err := LoadLockFile(p); return err },
		"pin":  func(p string) error { return CheckPinnedSHA256(p, sha) },
		"item": func(p string) error { return CheckFile(item, p) },
	}, data
}

func TestSupplyReadersRequireRegularFiles(t *testing.T) {
	readers, data := regularReaders(t)
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := read(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("directory = %v", err)
			}
			target := filepath.Join(dir, "real")
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := read(target); err != nil {
				t.Fatalf("regular input = %v", err)
			}
			link := filepath.Join(dir, "link")
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlink creation is unavailable: %v", err)
			}
			if err := read(link); err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("symlink = %v", err)
			}
		})
	}
}
