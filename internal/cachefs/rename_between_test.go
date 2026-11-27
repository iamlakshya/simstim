package cachefs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func betweenRoots(t *testing.T) (*os.Root, *os.Root, string, string) {
	t.Helper()
	base := isolatedRoot(t)
	fromPath, toPath := filepath.Join(base, "from"), filepath.Join(base, "to")
	from, err := OpenPrivateRoot(fromPath, true)
	if err != nil {
		t.Fatal(err)
	}
	to, err := OpenPrivateRoot(toPath, true)
	if err != nil {
		from.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = from.Close(); _ = to.Close() })
	return from, to, fromPath, toPath
}

func assertBetweenBytes(t *testing.T, root *os.Root, leaf string, body []byte) {
	t.Helper()
	got, err := root.ReadFile(leaf)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("anchored %s bytes = %q, %v", leaf, got, err)
	}
}

func TestRenameBetweenNoReplaceMovesActualInodeAcrossParents(t *testing.T) {
	from, to, _, _ := betweenRoots(t)
	body := []byte("caller-owned payload\x00\n")
	writeChecked(t, from, "part", body)
	file, err := OpenFile(from, "part", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := RenameBetweenNoReplace(from, "part", to, "published"); err != nil {
		t.Fatal(err)
	}
	if _, err := from.Lstat("part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source namespace did not commit its move", err)
	}
	after, err := to.Lstat("published")
	if err != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() || after.Size() != before.Size() {
		t.Fatal("cross-parent publication copied or changed the original inode", err)
	}
	assertBetweenBytes(t, to, "published", body)
	buffer := make([]byte, len(body))
	if _, err := file.ReadAt(buffer, 0); err != nil || !bytes.Equal(buffer, body) {
		t.Fatal("move took the caller's descriptor lifetime", err)
	}
}

func TestRenameBetweenNoReplacePreservesEveryExistingDestinationKind(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			from, to, _, toPath := betweenRoots(t)
			writeChecked(t, from, "part", []byte("incoming"))
			source, err := from.Lstat("part")
			if err != nil {
				t.Fatal(err)
			}
			foreign := filepath.Join(filepath.Dir(toPath), "foreign")
			if err := os.WriteFile(foreign, []byte("preserve foreign target"), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(toPath, "published")
			switch kind {
			case "file":
				writeChecked(t, to, "published", []byte("preserve destination"))
			case "directory":
				if err := to.Mkdir("published", 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(foreign, destination); err != nil {
					t.Fatal("fixture cannot establish native symlink semantics", err)
				}
			case "hardlink":
				if err := os.Link(foreign, destination); err != nil {
					t.Fatal("fixture cannot establish native hardlink semantics", err)
				}
			}
			before, err := to.Lstat("published")
			if err != nil {
				t.Fatal(err)
			}
			if err := RenameBetweenNoReplace(from, "part", to, "published"); err == nil {
				t.Fatal("no-clobber move replaced an existing destination", kind)
			}
			after, err := to.Lstat("published")
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("refused move changed the existing destination node", err)
			}
			retained, err := from.Lstat("part")
			if err != nil || !os.SameFile(source, retained) {
				t.Fatal("refused move unlinked its source", err)
			}
			assertBetweenBytes(t, from, "part", []byte("incoming"))
			if got, err := os.ReadFile(foreign); err != nil || string(got) != "preserve foreign target" {
				t.Fatal("move followed a link or changed foreign bytes", err)
			}
			if kind == "file" {
				assertBetweenBytes(t, to, "published", []byte("preserve destination"))
			}
		})
	}
}

func TestRenameBetweenNoReplaceUsesAnchorsAfterAmbientParentsAreReplaced(t *testing.T) {
	from, to, fromPath, toPath := betweenRoots(t)
	writeChecked(t, from, "part", []byte("original anchored source"))
	original, err := from.Lstat("part")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fromPath, toPath} {
		if err := os.Rename(path, path+".retained"); err != nil {
			t.Fatal("fixture cannot move an anchored owned directory", err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fromPath, "part"), []byte("foreign ambient source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toPath, "published"), []byte("foreign ambient destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameBetweenNoReplace(from, "part", to, "published"); err != nil {
		t.Fatal("move reconstructed ambient parent paths instead of using anchors", err)
	}
	published, err := to.Lstat("published")
	if err != nil || !os.SameFile(original, published) {
		t.Fatal("anchored result has another inode", err)
	}
	assertBetweenBytes(t, to, "published", []byte("original anchored source"))
	for path, expected := range map[string]string{filepath.Join(fromPath, "part"): "foreign ambient source", filepath.Join(toPath, "published"): "foreign ambient destination"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != expected {
			t.Fatal("anchored move touched a foreign ambient namespace", path, err)
		}
	}
}

func TestRenameBetweenNoReplaceUnlinkedDestinationNeverFallsBackToAmbientPath(t *testing.T) {
	from, to, _, toPath := betweenRoots(t)
	writeChecked(t, from, "part", []byte("retain source"))
	before, err := from.Lstat("part")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(toPath); err != nil {
		t.Fatal("fixture cannot unlink an empty anchored directory", err)
	}
	if err := os.Mkdir(toPath, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(toPath, "published")
	if err := os.WriteFile(foreign, []byte("foreign replacement parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameBetweenNoReplace(from, "part", to, "published"); err == nil {
		t.Fatal("unlinked target silently published into an ambient replacement")
	}
	after, err := from.Lstat("part")
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("unlinked-target refusal lost its source", err)
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "foreign replacement parent" {
		t.Fatal("unlinked-target refusal changed a foreign replacement", err)
	}
}

func TestRenameBetweenNoReplaceRejectsUnsafeLeavesWithoutChangingEitherRoot(t *testing.T) {
	from, to, _, _ := betweenRoots(t)
	writeChecked(t, from, "part", []byte("source"))
	writeChecked(t, to, "published", []byte("destination"))
	for _, invalid := range []string{"", ".", "..", "a/b", "a\\b", "/absolute", "C:leaf", "NUL", "leaf.", "leaf "} {
		if err := RenameBetweenNoReplace(from, invalid, to, "new"); err == nil {
			t.Fatal("unsafe source leaf accepted", invalid)
		}
		if err := RenameBetweenNoReplace(from, "part", to, invalid); err == nil {
			t.Fatal("unsafe destination leaf accepted", invalid)
		}
	}
	assertBetweenBytes(t, from, "part", []byte("source"))
	assertBetweenBytes(t, to, "published", []byte("destination"))
	if _, err := to.Lstat("new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid source caused a destination write", err)
	}
}

func TestRenameBetweenNoReplaceOriginalSameRootAPIStillSharesNoClobberSemantics(t *testing.T) {
	from, to, _, _ := betweenRoots(t)
	writeChecked(t, from, "part", []byte("same root"))
	before, err := from.Lstat("part")
	if err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(from, "part", "published"); err != nil {
		t.Fatal("original same-root API stopped working", err)
	}
	after, err := from.Lstat("published")
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("same-root API copied/replaced its inode", err)
	}
	writeChecked(t, to, "part", []byte("foreign attempt"))
	if err := RenameBetweenNoReplace(to, "part", from, "published"); err == nil {
		t.Fatal("cross-parent move overwrote an original-API publication")
	}
	assertBetweenBytes(t, from, "published", []byte("same root"))
	assertBetweenBytes(t, to, "part", []byte("foreign attempt"))
}
