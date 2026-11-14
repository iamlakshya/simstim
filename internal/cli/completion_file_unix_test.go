//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCompletionReadsTheValidatedDescriptorAfterPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	const original = "opened regular file"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := readCompletionData(opened)
	if !ok || string(got) != original {
		t.Fatalf("completion followed a replaced path: %q, %v", got, ok)
	}
}
