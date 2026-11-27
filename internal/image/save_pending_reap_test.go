package image

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This retires only ordinary fixture descriptors without its normal Close.
// It models orphan recovery data; it proves no process/VM exit or host crash.
func abandonPreparedDataFixture(t *testing.T, pending *PreparedSave) {
	t.Helper()
	pending.stopExpiry()
	if pending.output != nil {
		if err := pending.output.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if pending.metadata != nil && pending.metadata.Descriptor != nil {
		if err := pending.metadata.Descriptor.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if pending.root != nil {
		if err := pending.root.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if pending.pendingLease != nil {
		if err := pending.pendingLease.Close(); err != nil {
			t.Fatal(err)
		}
	}
	pending.closed = true
	close(pending.done)
}

func TestPreparedSavedReceiptReapsOnlyExpiredRetiredNativeData(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	pending := prepareSavedData(t, store, "tools", "ordinary", false)
	if report, err := store.reapSavedPending(t.Context(), time.Now().Add(2*SaveLifetime), false); err != nil || len(report.Removed) != 0 {
		t.Fatal("an age field displaced the actual live owner lease", err)
	}
	abandonPreparedDataFixture(t, pending)
	if report, err := store.reapSavedPending(t.Context(), time.Now(), false); err != nil || len(report.Removed) != 0 {
		t.Fatal("an unexpired native receipt was discarded", err)
	}
	report, err := store.reapSavedPending(t.Context(), time.Now().Add(2*SaveLifetime), false)
	if err != nil || len(report.Removed) != 1 || len(report.Left) != 0 {
		t.Fatal("expired exact native prepared data did not retire", err)
	}
	for _, name := range []string{pending.stage, pendingSaveLease(pending.id), "ownership/" + pendingSaveHead(pending.id), "ownership/" + pendingSaveExpiry(pending.id)} {
		if _, err := store.root.Lstat(name); !os.IsNotExist(err) {
			t.Fatal("expired original protocol resource remains", name, err)
		}
	}
}

func TestPreparedSavedRecoveryPreservesForeignReplacementAndUnreceiptedParts(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	pending := prepareSavedData(t, store, "tools", "ordinary", false)
	abandonPreparedDataFixture(t, pending)
	directory := filepath.Join(store.root.Name(), filepath.FromSlash(pending.stage))
	if err := os.Rename(filepath.Join(directory, "rootfs.img"), filepath.Join(directory, "original-elsewhere")); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("foreign data must remain")
	if err := os.WriteFile(filepath.Join(directory, "rootfs.img"), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	unreceipted := "incoming/.image-stage-cccccccccccccccccccccccccccccccc"
	if err := store.root.Mkdir(unreceipted, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := store.reapSavedPending(t.Context(), time.Now().Add(2*SaveLifetime), false)
	if err != nil || len(report.Removed) != 0 || len(report.Left) == 0 {
		t.Fatal("recovery adopted a replaced pending native path", err)
	}
	actual, err := os.ReadFile(filepath.Join(directory, "rootfs.img"))
	if err != nil || !bytes.Equal(actual, foreign) {
		t.Fatal("recovery changed foreign data", err)
	}
	if _, err := store.root.Lstat(unreceipted); err != nil {
		t.Fatal("recovery adopted an unreceipted building part", err)
	}
}

func TestPreparedSavedRecoveryRetiresMarkersAfterKeepWithoutDeletingTheImage(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	pending := prepareSavedData(t, store, "tools", "ordinary", false)
	kept, err := pending.Keep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	abandonPreparedDataFixture(t, pending)
	report, err := store.reapSavedPending(t.Context(), time.Now().Add(2*SaveLifetime), false)
	if err != nil || len(report.Left) != 0 {
		t.Fatal("a kept root left orphan pending markers", err)
	}
	request := Request{Ref: kept.Image.Ref, Digest: kept.Image.Digest, PlatformDigest: kept.Image.Digest, Arch: kept.Image.Arch, RootHash: kept.Image.RootHash, DataBlocks: kept.Image.DataBlocks, SavedHost: CloneSavedHostV1(kept.Image.SavedHost)}
	file, lease, _, err := store.OpenImageExisting(t.Context(), request)
	if err != nil {
		t.Fatal("marker cleanup deleted or relabelled the kept image", err)
	}
	file.Close()
	lease.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := pending.Keep(ctx); err == nil {
		t.Fatal("an idempotent keep renewed an ended lifetime")
	}
}

func TestSavedAliasLeaseRetiresOnlyAfterItsOwnedImageIsPurged(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	pending := prepareSavedData(t, store, "tools", "ordinary", false)
	if _, err := pending.Keep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := pending.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(report.Left) != 0 {
		t.Fatal("owned saved-image alias leases blocked complete native purge", err)
	}
}

func TestPreparedSavedRecoveryFinishesItsRecordedDirectoryAndMetadataCaptures(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	pending := prepareSavedData(t, store, "tools", "ordinary", false)
	abandonPreparedDataFixture(t, pending)
	owner, err := store.ownershipRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	receipt, _, err := store.readSavePending(owner, pendingSaveHead(pending.id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.createSavePending(owner, pendingSaveExpiry(pending.id), receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.root.Rename(pending.stage, pendingSaveCaptured(pending.id)); err != nil {
		t.Fatal(err)
	}
	root, err := store.childRoot(pendingSaveCaptured(pending.id))
	if err != nil {
		t.Fatal(err)
	}
	if err := captureLeaf(root, "rootfs.img", ".expired-root", receipt.Root, 0o600); err != nil {
		t.Fatal(err)
	}
	root.Close()
	report, err := store.reapSavedPending(t.Context(), time.Now().Add(2*SaveLifetime), false)
	if err != nil || len(report.Left) != 0 || len(report.Removed) != 1 {
		t.Fatal("known native expiry captures could not resume", err)
	}
	// A metadata unlink interrupted after its own capture is resumable too.
	second := prepareSavedData(t, store, "second", "ordinary", false)
	abandonPreparedDataFixture(t, second)
	if err := store.root.Remove(second.stage + "/rootfs.img"); err != nil {
		t.Fatal(err)
	}
	if err := store.root.Remove(second.stage); err != nil {
		t.Fatal(err)
	}
	_, witness, err := store.readSavePending(owner, pendingSaveHead(second.id))
	if err != nil {
		t.Fatal(err)
	}
	if err := captureLeaf(owner, pendingSaveHead(second.id), pendingSaveRetired(second.id), witness, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = store.reapSavedPending(t.Context(), time.Now().Add(2*SaveLifetime), false)
	if err != nil || len(report.Left) != 0 {
		t.Fatal("captured pending metadata could not finish retirement", err)
	}
}
