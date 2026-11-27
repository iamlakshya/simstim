//go:build linux

package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestCopyPlacementExcludesEveryPrivateAncestorAndForeignSession(t *testing.T) {
	isolate(t)
	session := paths.SessionDir("copy-own")
	own := filepath.Join(session, "work")
	foreign := filepath.Join(paths.SessionDir("copy-other"), "work")
	for _, name := range []string{own, foreign} {
		if err := os.MkdirAll(name, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{session, filepath.Dir(session), paths.StateDir(), foreign} {
		if allowed, err := copyPlacementV3(session, []wall.Mount{{Host: source, Guest: "/work", Write: true}}); err == nil || allowed {
			t.Fatalf("private source %q was admitted", source)
		}
	}
	if allowed, err := copyPlacementV3(session, []wall.Mount{{Host: own, Guest: "/work", Write: true}}); err != nil || !allowed {
		t.Fatalf("constructor work sibling was refused: %v", err)
	}
	// A free-standing private-looking path is not a protected factory realm.
	if allowed, err := copyPlacementV3(t.TempDir(), nil); err != nil || allowed {
		t.Fatalf("caller path issued protected placement: %v", err)
	}
}

func TestCopySharedSourceNeverContainsEitherFactoryStageAndBootstrapCleanupJoins(t *testing.T) {
	isolate(t)
	shared := t.TempDir()
	source, err := pinSource(shared)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, id := range []string{"copy-first", "copy-second"} {
		session := paths.SessionDir(id)
		if err := os.MkdirAll(session, 0700); err != nil {
			t.Fatal(err)
		}
		allowed, err := copyPlacementV3(session, []wall.Mount{{Host: shared, Guest: "/mnt/shared", Write: true}})
		if err != nil || !allowed {
			t.Fatal("shared ordinary source lost private placement", err)
		}
		storage, err := os.OpenRoot(session)
		if err != nil {
			t.Fatal(err)
		}
		stage, extra, err := prepareCopyMountV3(storage, source)
		if err != nil {
			t.Fatal(err)
		}
		if err := extra.Close(); err != nil {
			t.Fatal(err)
		}
		contains, err := copyContainsV3(source, stage.stage)
		if err != nil || contains {
			t.Fatal("shared guest source reaches staged bytes", err)
		}
		// A bootstrap failure has no child to send the directory witness. The
		// actual parent already holds it and closes/cleans it independently.
		owner, child, err := copyControlV1(&sandbox{})
		if err != nil {
			t.Fatal(err)
		}
		owner.native.mounts[0] = stage
		if err := owner.native.nativeExit(); err != nil {
			t.Fatal(err)
		}
		if err := child.Close(); err != nil {
			t.Fatal(err)
		}
		if entries, err := os.ReadDir(session); err != nil || len(entries) != 0 {
			t.Fatal("bootstrap left original staged storage", entries, err)
		}
		if err := storage.Close(); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(shared)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".copy-") {
			t.Fatal("factory staged under a shared guest source")
		}
	}
}
