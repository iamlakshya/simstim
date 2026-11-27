//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPurgePublicCodeRequiresActualReadableBytesV3(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "original")
	second := filepath.Join(directory, "relay")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("original public code bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	info, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := os.Open(second)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	other, err := candidate.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, other) {
		t.Fatal("fixture did not create independent native inodes")
	}
	if err := checkPurgePublicCodeBytesV3(context.Background(), original, candidate, info); err != nil {
		t.Fatal("same public bytes in another readable inode were refused", err)
	}
	writable, err := os.OpenFile(second, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPurgePublicCodeBytesV3(context.Background(), original, writable, info); err == nil {
		t.Fatal("a writable descriptor supplied the readonly public role")
	}
	if _, err := writable.WriteAt([]byte("substitute"), 0); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkPurgePublicCodeBytesV3(context.Background(), original, candidate, info); err == nil {
		t.Fatal("different direct bytes passed the public code filter")
	}
	if err := os.WriteFile(second, []byte("original public code bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if err := checkPurgePublicCodeBytesV3(context.Background(), original, candidate, info); err != nil {
		t.Fatal("the same retained readable public object was lost after unlink", err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("original public code bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Open(second)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := checkPurgePublicCodeBytesV3(context.Background(), original, candidate, info); err == nil {
		t.Fatal("a closed original object was revived by a reused descriptor number")
	}
}
