//go:build linux

package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func rewindPartDurabilityFixtureV1(t *testing.T, rolledBack bool) (*rewindMetadataPartV1, *os.Root) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := cachefs.CaptureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	file, err := cachefs.OpenFile(root, "manifest.json", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("original member data"); err != nil {
		t.Fatal(err)
	}
	fileRef, err := cachefs.CaptureFile(root, "manifest.json", file, 0600)
	if err != nil {
		t.Fatal(err)
	}
	part := &rewindMetadataPartV1{root: &rewindMemberRootV1{root: root, rootRef: ref}, published: true, rolledBack: rolledBack}
	if rolledBack {
		part.savedBefore, part.savedBeforeRef = file, fileRef
	} else {
		part.after, part.afterRef = file, fileRef
	}
	t.Cleanup(func() { _ = file.Close(); _ = root.Close() })
	// This is a file-durability primitive fixture, not an actual stopped
	// session or a permission to publish/restore any trusted manifest.
	return part, root
}

func TestRewindMetadataRetryRechecksCanonicalObjectAfterPhysicalRename(t *testing.T) {
	for _, rolledBack := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "rolled-back"}[rolledBack], func(t *testing.T) {
			part, root := rewindPartDurabilityFixtureV1(t, rolledBack)
			check := part.checkPublishedV1
			if rolledBack {
				check = part.checkRolledBackV1
			}
			if err := check(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := root.Rename("manifest.json", "retained-original"); err != nil {
				t.Fatal(err)
			}
			replacement, err := cachefs.OpenFile(root, "manifest.json", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer replacement.Close()
			if err := check(t.Context()); err == nil {
				t.Fatal("a prior physical-progress flag hid a substituted canonical member")
			}
			if _, err := root.Lstat("manifest.json"); err != nil {
				t.Fatal("a failed retry removed the unknown canonical replacement", err)
			}
		})
	}
}

func TestRewindMetadataRetryCannotSkipLostDirectorySyncOwner(t *testing.T) {
	for _, rolledBack := range []bool{false, true} {
		part, root := rewindPartDurabilityFixtureV1(t, rolledBack)
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		if rolledBack {
			err = part.checkRolledBackV1(t.Context())
		} else {
			err = part.checkPublishedV1(t.Context())
		}
		if err == nil {
			t.Fatal("an already-renamed part advanced after its actual sync root closed")
		}
	}
}

func rewindJournalCleanupFixtureV1(t *testing.T) (*rewindJournalOwnerV1, *os.File) {
	t.Helper()
	isolatedStoreHostConstruction(t)
	m := &Manifest{Schema: 1, ID: "parent23", Name: "original", CreatedAt: time.Now().UTC()}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	operation, err := BeginSessionOperation(t.Context(), m.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(operation.Close)
	file, err := cachefs.OpenFile(operation.native.root, rewindJournalLeafV1, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("journal data only"); err != nil {
		t.Fatal(err)
	}
	ref, err := cachefs.CaptureFile(operation.native.root, rewindJournalLeafV1, file, 0600)
	if err != nil {
		t.Fatal(err)
	}
	p := &bridgeRewindPreparationV1{operation: operation}
	p.active.Store(true)
	j := &rewindJournalOwnerV1{preparation: p, file: file, ref: ref, data: rewindJournalDataV1{Phase: "completed"}}
	t.Cleanup(func() { _ = file.Close() })
	// The real OP and created file are sufficient only for their own data
	// cleanup. This fixture creates no stopped, Cold or runtime authority.
	return j, file
}

func TestRewindJournalUnlinkDoesNotHidePendingSyncAndClose(t *testing.T) {
	j, file := rewindJournalCleanupFixtureV1(t)
	if err := j.preparation.operation.native.root.Remove(rewindJournalLeafV1); err != nil {
		t.Fatal(err)
	}
	j.removed = true // The physical state after an unlink succeeded.
	if j.removalDoneV1() {
		t.Fatal("a physical unlink was treated as a durable completed cleanup")
	}
	if err := j.removeV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !j.removeSynced || !j.fileClosed || !j.removalDoneV1() {
		t.Fatal("cleanup retry failed to record both sync and actual file close")
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("the journal's original descriptor remained open", err)
	}
	if err := j.removeV1(t.Context()); err != nil {
		t.Fatal("repeated cleanup closed an already closed owned descriptor", err)
	}
}

func TestRewindJournalCloseFailureCannotBecomeSuccessOnRetry(t *testing.T) {
	j, file := rewindJournalCleanupFixtureV1(t)
	if err := j.preparation.operation.native.root.Remove(rewindJournalLeafV1); err != nil {
		t.Fatal(err)
	}
	j.removed = true
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.removeV1(t.Context()); err == nil {
		t.Fatal("cleanup accepted an unexpected original-descriptor closure")
	}
	if err := j.removeV1(t.Context()); err == nil || j.removalDoneV1() {
		t.Fatal("a failed native file close disappeared on retry")
	}
}
