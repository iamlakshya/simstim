package checkpoint

import (
	"testing"
	"time"
)

func closeErrorFixtureV1(t *testing.T) (*fixture, time.Time) {
	t.Helper()
	f := newFixture(t)
	f.write(t, "snapshot bytes")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.store.Create(t.Context(), at, metadata("conversation"), 10); err != nil {
		t.Fatal(err)
	}
	return f, at
}

func TestSelectionCloseFailureIsSticky(t *testing.T) {
	f, _ := closeErrorFixtureV1(t)
	selection, err := f.store.Resolve(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := selection.disk.Close(); err != nil {
		t.Fatal(err)
	}
	if err := selection.Close(); err == nil {
		t.Fatal("an unexpected selection descriptor closure was accepted")
	}
	if err := selection.Close(); err == nil {
		t.Fatal("a selection close failure disappeared on retry")
	}
}

func TestReadonlySnapshotAndPreparedCloseFailuresAreSticky(t *testing.T) {
	f, at := closeErrorFixtureV1(t)
	selected, err := NewLocalBackendV1(f.store).Select(t.Context(), at.Format(TimeLayout))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := selected.(*localSnapshotV1)
	if err := snapshot.disk.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err == nil {
		t.Fatal("an unexpected read-cap descriptor closure was accepted")
	}
	if err := snapshot.Close(); err == nil {
		t.Fatal("a snapshot close failure disappeared on retry")
	}
	// This is only the settled read-cap cleanup wrapper. It issues no disk
	// replacement, native stopped ownership or permission to restart.
	prepared := &localPreparedRestoreV1{store: f.store, snapshot: snapshot, journal: TransactionV1{Phase: RestoreRolledBackV1}}
	if err := prepared.Close(); err == nil {
		t.Fatal("a prepared owner lost its selected read-cap close failure")
	}
	if err := prepared.Close(); err == nil {
		t.Fatal("a prepared-owner close failure disappeared on retry")
	}
}
