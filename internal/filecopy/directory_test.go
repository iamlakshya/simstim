package filecopy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type directoryEndReader struct {
	before func()
	err    error
}

func (r directoryEndReader) Read([]byte) (int, error) {
	if r.before != nil {
		r.before()
	}
	return 0, r.err
}

func TestMakeDirectoryCreatesAnActualNestedEmptyDirectory(t *testing.T) {
	root, _ := batchRoot(t)
	if err := MakeDirectory(t.Context(), root, "one/two/empty", bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "one/two", "one/two/empty"} {
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("ordinary rooted directory missing: %q %v %v", name, info, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&^os.FileMode(0755) != 0 {
			t.Fatalf("new directory widened its fixed permissions: %q %v", name, info.Mode())
		}
	}
	file, err := root.Open("one/two/empty")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := file.ReadDir(-1)
	if err = errors.Join(err, file.Close()); err != nil || len(entries) != 0 {
		t.Fatalf("declared empty directory is not empty: %v %v", entries, err)
	}
}

func TestMakeDirectoryChecksExactEmptyEOFAndCancellationBeforeWrites(t *testing.T) {
	for _, mode := range []string{"payload", "read-error", "not-eof", "cancelled", "cancel-on-eof"} {
		t.Run(mode, func(t *testing.T) {
			root, directory := batchRoot(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var input io.Reader = bytes.NewReader(nil)
			switch mode {
			case "payload":
				input = bytes.NewReader([]byte{1})
			case "read-error":
				input = directoryEndReader{err: io.ErrUnexpectedEOF}
			case "not-eof":
				input = directoryEndReader{}
			case "cancelled":
				cancel()
			case "cancel-on-eof":
				input = directoryEndReader{before: cancel, err: io.EOF}
			}
			if err := MakeDirectory(ctx, root, "new/tree", input); err == nil {
				t.Fatal("a non-empty, unfinished or cancelled declaration created directories")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected declaration changed the destination: %v %v", entries, err)
			}
		})
	}
}

func TestMakeDirectoryNeverReplacesExistingFilesOrLinks(t *testing.T) {
	root, directory := batchRoot(t)
	if err := root.WriteFile("ordinary", []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ordinary", "ordinary/nested"} {
		if err := MakeDirectory(t.Context(), root, name, bytes.NewReader(nil)); err == nil {
			t.Fatalf("an existing file became a directory: %s", name)
		}
	}
	data, err := root.ReadFile("ordinary")
	if err != nil || string(data) != "preserved" {
		t.Fatal("directory creation changed an existing file", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, "link")); err != nil {
		t.Skipf("native symlink fixture unavailable: %v", err)
	}
	for _, name := range []string{"link", "link/nested"} {
		if err := MakeDirectory(t.Context(), root, name, bytes.NewReader(nil)); err == nil {
			t.Fatalf("directory creation followed an existing link: %s", name)
		}
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory creation changed a link target: %v %v", entries, err)
	}
}

func TestMakeDirectoryReusesOnlyAnExistingOrdinaryDirectory(t *testing.T) {
	root, _ := batchRoot(t)
	if err := root.Mkdir("ordinary", 0700); err != nil {
		t.Fatal(err)
	}
	before, err := root.Lstat("ordinary")
	if err != nil {
		t.Fatal(err)
	}
	if err := MakeDirectory(t.Context(), root, "ordinary", bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	after, err := root.Lstat("ordinary")
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("reusing an ordinary directory replaced or changed it", err)
	}
}

func TestMakeDirectoryRejectsAnUnsafeWholePathAndClosedRoot(t *testing.T) {
	root, directory := batchRoot(t)
	for _, relative := range []string{"", ".", "../outside", "one/../outside", "/absolute", "new/.git/hooks", "new//empty"} {
		if err := MakeDirectory(t.Context(), root, relative, bytes.NewReader(nil)); err == nil {
			t.Fatalf("unsafe directory declaration accepted: %q", relative)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsafe whole-path declarations created a prefix", err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MakeDirectory(t.Context(), root, "closed", bytes.NewReader(nil)); err == nil {
		t.Fatal("directory creation accepted a closed root")
	}
	if err := MakeDirectory(t.Context(), nil, "nil", bytes.NewReader(nil)); err == nil {
		t.Fatal("directory creation accepted a missing root")
	}
	if err := MakeDirectory(t.Context(), root, "nil", nil); err == nil {
		t.Fatal("directory creation accepted a missing stream")
	}
}
