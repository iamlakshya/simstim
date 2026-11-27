package image

import (
	"errors"
	"os"
	"testing"
)

func TestTransferCleanupRetriesSameOriginalCreatorAfterReplacementV1(t *testing.T) {
	isolateImageOwnership(t)
	store := imageStore(t)
	_, request, _, layers, _ := imageRegistryFixture(t)
	transfer, err := store.Begin(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := layers[0].Digest
	if err := transfer.WriteAt(t.Context(), digest, 0, []byte("original")); err != nil {
		t.Fatal(err)
	}
	original := transfer.files[digest]
	name := "incoming/" + transfer.token + "/" + digest[7:] + ".part"
	if err := store.root.Rename(name, name+"-held"); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.regular(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.Write([]byte("foreign")); err != nil {
		replacement.Close()
		t.Fatal(err)
	}
	replacement.Close()
	if err := transfer.Close(); !errors.Is(err, ErrTransferCleanupPending) {
		t.Fatalf("uncertain cleanup lost its pending owner: %v", err)
	}
	if transfer.files[digest] != original {
		t.Fatal("cleanup discarded its original creator capability")
	}
	if _, err := original.Stat(); err != nil {
		t.Fatalf("original descriptor was closed before uncertain cleanup: %v", err)
	}
	if err := store.root.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := store.root.Rename(name+"-held", name); err != nil {
		t.Fatal(err)
	}
	if err := transfer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("completed cleanup retained an original writer", err)
	}
	if _, err := store.root.Lstat("incoming/" + transfer.token); !os.IsNotExist(err) {
		t.Fatal("retried original transfer directory remains", err)
	}
	if err := transfer.Close(); err != nil {
		t.Fatal("completed cleanup was not idempotent", err)
	}
	if err := transfer.WriteAt(t.Context(), digest, 0, []byte("late")); err == nil {
		t.Fatal("closed transfer recreated a writer")
	}
}
func TestTransferCommitReportsCleanupPendingAndRetriesOriginalDirectoryV1(t *testing.T) {
	isolateImageOwnership(t)
	store := imageStore(t)
	_, request, _, _, _ := imageRegistryFixture(t)
	transfer, err := store.Begin(request)
	if err != nil {
		t.Fatal(err)
	}
	name := "incoming/" + transfer.token + "/foreign"
	foreign, err := store.regular(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	foreign.Close()
	// Missing documents refuse before a converter is invoked. Cleanup of the
	// unrelated entry must still be reported, with the original directory held.
	if _, err := transfer.Commit(t.Context(), nil); !errors.Is(err, ErrTransferCleanupPending) {
		t.Fatal("Commit hid unfinished cleanup behind its ordinary failure", err)
	}
	if transfer.directoryRoot == nil {
		t.Fatal("pending cleanup discarded the original directory handle")
	}
	if err := store.root.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := transfer.Close(); err != nil {
		t.Fatal(err)
	}
}
