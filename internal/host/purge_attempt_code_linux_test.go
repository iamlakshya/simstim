//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestNativeCodeComparisonUsesActualPreopenedExecuteOnlyDescriptorsV2(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "original"), filepath.Join(root, "copy")
	if err := os.WriteFile(first, []byte("actual-code-bytes"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("actual-code-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	copied, err := os.Open(second)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	before, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(second, 0o100); err != nil {
		t.Fatal(err)
	}
	if err := verifyOriginalPurgeCodeBytesV2(context.Background(), original, copied, before); err != nil {
		t.Fatal("preopened exact native bytes were not readable after sealing", err)
	}
	info, err := copied.Stat()
	if err != nil || info.Mode().Perm() != 0o100 {
		t.Fatal("copied code was not execute-only", info, err)
	}
	if err := os.Chmod(second, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("foreign-code-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyOriginalPurgeCodeBytesV2(context.Background(), original, copied, before); err == nil {
		t.Fatal("changed native copy bytes were accepted from matching role metadata")
	}
}

func TestTemporaryCodeCreatorPreservesReplacedNamedFileV2(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := cachefs.OpenFile(root, "code", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	created, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reference, err := cachefs.CaptureFile(root, "code", file, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(path, "code"), filepath.Join(path, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "code"), []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf := "code"
	if err := removeOwnPurgeCodeFileV2(root, file, created, reference, &leaf, ".remove-code"); err == nil {
		t.Fatal("a replaced code pathname renewed original creator ownership")
	}
	raw, err := os.ReadFile(filepath.Join(path, "code"))
	if err != nil || string(raw) != "unrelated" {
		t.Fatal("creator cleanup changed the unrelated replacement", err)
	}
}

func TestTemporaryPurgeCodeCannotOriginateFromMetadataV2(t *testing.T) {
	if owner, err := newPurgeAttemptCodeV2(context.Background(), nil, api.PurgeArgs{}); owner != nil || err == nil {
		t.Fatal("metadata created a temporary code factory")
	}
	for _, owner := range []*purgeAttemptCodeV2{nil, {}, {sealed: true}} {
		if err := owner.Check(context.Background()); err == nil {
			t.Fatal("phase metadata created native code ownership")
		}
	}
}
