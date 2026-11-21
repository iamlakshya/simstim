package checkpoint

import (
	"os"
	"testing"
	"time"
)

func TestRollbackRemovesOnlySameStoreCreatedCheckpoint(t *testing.T) {
	f := newFixture(t)
	f.write(t, "checkpoint bytes")
	entry, err := f.store.Create(t.Context(), time.Now().UTC(), metadata("conversation"), 0)
	if err != nil {
		t.Fatal(err)
	}
	other := entry
	other.Time = entry.Time.Add(time.Millisecond)
	if err := f.store.RemoveCreated(t.Context(), other); err == nil {
		t.Fatal("another checkpoint name granted rollback authority")
	}
	if err := f.store.RemoveCreated(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	entries, err := f.store.List(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed copy remained listed: %v %v", entries, err)
	}
}

func TestRollbackPreservesSubstitutedCheckpointLeaf(t *testing.T) {
	f := newFixture(t)
	f.write(t, "original")
	entry, err := f.store.Create(t.Context(), time.Now().UTC(), metadata("conversation"), 0)
	if err != nil {
		t.Fatal(err)
	}
	name := entry.Time.Format(TimeLayout) + ".disk"
	if err := f.store.entries.Rename(name, "retained.disk"); err != nil {
		t.Fatal(err)
	}
	file, err := f.store.entries.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := f.store.RemoveCreated(t.Context(), entry); err == nil {
		t.Fatal("rollback adopted a substituted checkpoint file")
	}
	if _, err := f.store.entries.Lstat(name); err != nil {
		t.Fatal("rollback removed the substituted file")
	}
}
