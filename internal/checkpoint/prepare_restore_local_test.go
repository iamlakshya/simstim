package checkpoint

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func stoppedLocalBackendV1(t *testing.T) (*fixture, *LocalBackendV1, SnapshotV1, TransactionV1) {
	t.Helper()
	f := newFixture(t)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.write(t, "old files")
	if _, err := f.store.Create(t.Context(), at, metadata("old-conversation"), 10); err != nil {
		t.Fatal(err)
	}
	running := NewLocalBackendV1(f.store)
	selected, err := running.Select(t.Context(), at.Format(TimeLayout))
	if err != nil {
		t.Fatal(err)
	}
	clone := f.store.clone
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	// A real running borrow has now ended. The read-only snapshot alone can
	// cross the stop; it grants no permission to open this stopped backend.
	f.write(t, "new files")
	f.stopped = true
	stopped, err := OpenOwned(t.Context(), f.session, f.vm, f.disk, f.checkStopped)
	if err != nil {
		t.Fatal(err)
	}
	stopped.clone = clone
	f.store = stopped
	transaction := TransactionV1{Version: 1, Generation: strings.Repeat("a", 32), Safety: Entry{Time: at.Add(time.Minute), Metadata: metadata("new-conversation")}}
	t.Cleanup(func() { _ = selected.Close() })
	return f, NewLocalBackendV1(stopped), selected, transaction
}

func requireNativeExchangeV1(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("native stopped disk exchange runs only in the Linux runtime")
	}
}

func TestPreparedRestoreRetainsReadSnapshotAcrossOriginalStoreClosure(t *testing.T) {
	f, backend, selected, transaction := stoppedLocalBackendV1(t)
	prepared, err := backend.PrepareRestore(t.Context(), selected, transaction, f.checkStopped)
	if err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	journal := prepared.Journal()
	if journal.Phase != RestorePreparedV1 || journal.Selected.Metadata.AgentSessionIDs["session"] != "old-conversation" || journal.Safety.Metadata.AgentSessionIDs["session"] != "new-conversation" {
		t.Fatalf("prepared rewind lost one conversation: %#v", journal)
	}
	current, err := io.ReadAll(io.NewSectionReader(f.disk, 0, 100))
	if err != nil || string(current) != "new files" {
		t.Fatalf("preparation changed the canonical disk before host CAS: %q, %v", current, err)
	}
	journal.Selected.Metadata.AgentSessionIDs["session"] = "changed data copy"
	if prepared.Journal().Selected.Metadata.AgentSessionIDs["session"] != "old-conversation" {
		t.Fatal("journal data aliased the retained snapshot")
	}
	if err := prepared.Close(); err == nil {
		t.Fatal("an unsettled prepared transaction released disk custody")
	}
	if err := f.store.Close(); err == nil {
		t.Fatal("the store closed while a transaction retained its native roots")
	}
	if err := backend.Trim(t.Context(), 1); err == nil {
		t.Fatal("retention ran before the prepared transaction settled")
	}
	if err := prepared.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRestoreRollbackReturnsExactOriginalInode(t *testing.T) {
	requireNativeExchangeV1(t)
	f, backend, selected, transaction := stoppedLocalBackendV1(t)
	original, err := f.disk.Stat()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := backend.PrepareRestore(t.Context(), selected, transaction, f.checkStopped)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	journal := prepared.Journal()
	oldNamed, err := f.vm.Lstat(journal.Original.Name)
	if err != nil || !os.SameFile(original, oldNamed) {
		t.Fatalf("commit lost the exact named original inode: %v, %v", oldNamed, err)
	}
	if _, err := cachefs.ValidateFile(f.vm, journal.Original.Name, f.disk, 0o600); err != nil {
		t.Fatal("commit lost the original inode's private single-link identity", err)
	}
	if err := prepared.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := f.vm.Lstat(diskName)
	if err != nil || !os.SameFile(original, current) {
		t.Fatalf("rollback reconstructed rather than restored the exact original: %v, %v", current, err)
	}
	if prepared.Journal().Phase != RestoreRolledBackV1 {
		t.Fatal("rollback journal did not describe actual disk progress")
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRestoreRetainsBothDisksAfterPostExchangeOwnerFailure(t *testing.T) {
	requireNativeExchangeV1(t)
	f, backend, selected, transaction := stoppedLocalBackendV1(t)
	original, err := f.disk.Stat()
	if err != nil {
		t.Fatal(err)
	}
	uncertain := false
	check := func(ctx context.Context) error {
		if err := f.checkStopped(ctx); err != nil {
			return err
		}
		current, err := f.vm.Lstat(diskName)
		if err != nil {
			return err
		}
		if uncertain && !os.SameFile(original, current) {
			return errors.New("the actual stopped issuer requires reconciliation")
		}
		return nil
	}
	prepared, err := backend.PrepareRestore(t.Context(), selected, transaction, check)
	if err != nil {
		t.Fatal(err)
	}
	uncertain = true
	if err := prepared.Commit(t.Context()); err == nil {
		t.Fatal("commit silently accepted a failed post-exchange stopped proof")
	}
	journal := prepared.Journal()
	if journal.Phase != RestoreExchangedV1 {
		t.Fatal("a post-exchange error forgot physical progress")
	}
	if _, err := f.vm.Lstat(journal.Original.Name); err != nil {
		t.Fatal("a post-exchange error dropped the retained original disk", err)
	}
	if _, err := f.vm.Lstat(journal.Replacement.Name); err != nil {
		t.Fatal("a post-exchange error dropped the retained replacement disk", err)
	}
	if err := prepared.Close(); err == nil {
		t.Fatal("an uncertain physical exchange released its native disk custody")
	}
	uncertain = false
	if err := prepared.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRestoreFinalizationKeepsReplacementInStoreCustody(t *testing.T) {
	requireNativeExchangeV1(t)
	f, backend, selected, transaction := stoppedLocalBackendV1(t)
	prepared, err := backend.PrepareRestore(t.Context(), selected, transaction, f.checkStopped)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finalize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Check(t.Context()); err != nil {
		t.Fatal("settled transaction lost the actual canonical replacement descriptor", err)
	}
	if prepared.Journal().Phase != RestoreFinalizedV1 {
		t.Fatal("finalized disk progress was not retained as journal data")
	}
}

type forgedSnapshotV1 struct{ value Entry }

func (snapshot forgedSnapshotV1) Entry() Entry { return snapshot.value }

func (forgedSnapshotV1) DiskBytesV1() int64            { return 4096 }
func (forgedSnapshotV1) CheckV1(context.Context) error { return nil }

func (forgedSnapshotV1) Close() error { return nil }

func TestEntryAndJournalDataCannotRecreateLocalSnapshot(t *testing.T) {
	f, backend, selected, transaction := stoppedLocalBackendV1(t)
	if prepared, err := backend.PrepareRestore(t.Context(), forgedSnapshotV1{value: selected.Entry()}, transaction, f.checkStopped); err == nil || prepared != nil {
		t.Fatal("copied checkpoint data reconstructed a local read capability")
	}
	if prepared, err := backend.PrepareRestore(t.Context(), selected, transaction, nil); err == nil || prepared != nil {
		t.Fatal("journal data reconstructed the original stopped issuer")
	}
}
