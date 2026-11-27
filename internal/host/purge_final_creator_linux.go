//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This is issued at the real O_EXCL final marker creation, not from its saved
// witness or a report. The original file/root descriptors remain with the same
// transaction through the final result and physical subject settlement.
type purgeFinalGuardV2 struct {
	mu       sync.Mutex
	owner    *purgeOwnerV1
	root     *os.Root
	rootRef  cachefs.Reference
	marker   *os.File
	created  cachefs.Reference
	sealed   cachefs.Reference
	leaf     string
	body     []byte
	closed   bool
	closeErr error
}

func retainCreatedPurgeFinalMarkerV2(owner *purgeOwnerV1, guard *os.Root, marker *os.File, leaf string) (bool, error) {
	if owner == nil || owner.ctx == nil {
		return false, placementError("the final marker has no actual purge writer")
	}
	transaction, _ := owner.ctx.Value(purgeActualTransactionKeyV2{}).(*purgeActiveTransactionV2)
	if transaction == nil {
		return false, nil
	} // Existing unretained caller keeps its current behaviour.
	if transaction.owner != owner || transaction.closed || transaction.origin == nil || transaction.finalGuard != nil {
		return false, placementError("the final marker is not this SAME actual transaction's first creator")
	}
	created := &purgeFinalGuardV2{owner: owner, marker: marker, leaf: leaf}
	transaction.finalGuard = created // Adopt the actual native FD BEFORE errors.
	// Borrow the already-retained original control root. It was pinned before
	// capture; no failing post-creation dup may orphan this actual marker.
	transaction.origin.mu.Lock()
	for _, root := range transaction.origin.roots {
		if root.path == owner.placement.control {
			created.root = root.root
			break
		}
	}
	transaction.origin.mu.Unlock()
	if created.root == nil {
		return true, placementError("the final creator lost its original retained control root")
	}
	var err error
	created.rootRef, err = cachefs.CaptureRoot(created.root)
	if err != nil {
		return true, err
	}
	created.created, err = cachefs.CaptureFile(created.root, leaf, marker, 0o600)
	if err != nil {
		return true, err
	}
	return true, nil
}

func sealCreatedPurgeFinalMarkerV2(owner *purgeOwnerV1, body []byte) error {
	transaction, _ := owner.ctx.Value(purgeActualTransactionKeyV2{}).(*purgeActiveTransactionV2)
	if transaction == nil {
		return nil
	}
	created := transaction.finalGuard
	if created == nil || created.owner != owner {
		return placementError("the actual final writer lost its original marker creation")
	}
	created.mu.Lock()
	defer created.mu.Unlock()
	if created.closed || created.root == nil || created.marker == nil || len(body) == 0 {
		return placementError("the actual final marker creation is incomplete")
	}
	if err := created.created.CheckCreatedFile(created.root, created.leaf, created.marker, 0o600); err != nil {
		return err
	}
	if err := checkActualMarkerBytesV2(created.marker, body); err != nil {
		return err
	}
	sealed, err := cachefs.CaptureFile(created.root, created.leaf, created.marker, 0o600)
	if err != nil {
		return err
	}
	created.body, created.sealed = bytes.Clone(body), sealed
	return nil
}

func checkActualMarkerBytesV2(marker *os.File, expected []byte) error {
	if marker == nil || len(expected) == 0 {
		return placementError("the actual marker byte comparison is absent")
	}
	info, err := marker.Stat()
	if err != nil || info.Size() != int64(len(expected)) {
		return errors.Join(placementError("the actual original marker changed its exact byte length"), err)
	}
	raw := make([]byte, len(expected)+1)
	n, err := marker.ReadAt(raw, 0)
	if err != nil && !errors.Is(err, io.EOF) || !bytes.Equal(raw[:n], expected) {
		return errors.Join(placementError("the original native marker changed its exact contents"), err)
	}
	return nil
}

func (created *purgeFinalGuardV2) checkLockedV2(ctx context.Context) error {
	if created == nil || ctx == nil || created.closed || created.owner == nil || created.root == nil || created.marker == nil || !created.sealed.Known() || len(created.body) == 0 {
		return placementError("the original final marker has not actually sealed")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), created.rootRef.CheckRoot(created.root), checkActualMarkerBytesV2(created.marker, created.body)); err != nil {
		return err
	}
	info, err := created.marker.Stat()
	if err != nil {
		return err
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known {
		return placementError("the original final marker has unknown native link state")
	}
	_, err = created.root.Lstat(created.leaf)
	if errors.Is(err, os.ErrNotExist) && links == 0 {
		// A later fixed terminal acknowledgement may unlink this SAME original
		// marker. Its actual retained FD and bytes, not the DTO, survive it.
		return nil
	}
	if err != nil {
		return err
	}
	return created.sealed.CheckFile(created.root, created.leaf, created.marker, 0o600)
}

// Check requires the actual original creator and its sealed native object.
func (created *purgeFinalGuardV2) Check(ctx context.Context) error {
	if created == nil {
		return placementError("the actual final marker creator is absent")
	}
	created.mu.Lock()
	defer created.mu.Unlock()
	return created.checkLockedV2(ctx)
}

// Close releases only this writer's original read descriptors, after the
// transaction has independently checked all original physical cleanup.
func (created *purgeFinalGuardV2) Close() error {
	if created == nil {
		return nil
	}
	created.mu.Lock()
	defer created.mu.Unlock()
	if created.closed {
		return created.closeErr
	}
	if err := created.checkLockedV2(context.Background()); err != nil {
		return err
	}
	created.closeErr = created.marker.Close()
	created.closed = true
	return created.closeErr
}

func reuseCreatedPurgeFinalMarkerV2(owner *purgeOwnerV1, guard *os.Root) (*os.File, string, string, bool, error) {
	transaction, _ := owner.ctx.Value(purgeActualTransactionKeyV2{}).(*purgeActiveTransactionV2)
	if transaction == nil || transaction.finalGuard == nil {
		return nil, "", "", false, nil
	}
	created := transaction.finalGuard
	created.mu.Lock()
	defer created.mu.Unlock()
	if created.owner != owner || created.closed || created.root == nil || created.marker == nil {
		return nil, "", "", true, placementError("the same final writer lost its actual partial creation")
	}
	if !created.rootRef.Known() {
		var err error
		created.rootRef, err = cachefs.CaptureRoot(created.root)
		if err != nil {
			return nil, "", "", true, err
		}
	}
	if !created.created.Known() {
		var err error
		created.created, err = cachefs.CaptureFile(created.root, created.leaf, created.marker, 0o600)
		if err != nil {
			return nil, "", "", true, err
		}
	}
	if err := errors.Join(created.rootRef.CheckRoot(guard), created.created.CheckCreatedFile(created.root, created.leaf, created.marker, 0o600)); err != nil {
		return nil, "", "", true, err
	}
	if created.sealed.Known() {
		return nil, "", "", true, placementError("the actual sealed final marker must use its same completed result")
	}
	token := strings.TrimPrefix(created.leaf, ".final-marker-")
	if !ValidHostID(token) || created.leaf != ".final-marker-"+token {
		return nil, "", "", true, placementError("the original marker creation changed its actual continuation")
	}
	// Only this actual retained unsealed creator can restart its own bytes.
	if err := created.marker.Truncate(0); err != nil {
		return nil, "", "", true, err
	}
	if _, err := created.marker.Seek(0, io.SeekStart); err != nil {
		return nil, "", "", true, err
	}
	return created.marker, created.leaf, token, true, nil
}

func actualPurgeFinalMarkerReadyV2(owner *purgeOwnerV1) (bool, error) {
	transaction, _ := owner.ctx.Value(purgeActualTransactionKeyV2{}).(*purgeActiveTransactionV2)
	if transaction == nil {
		return true, nil
	}
	created := transaction.finalGuard
	if created == nil || created.owner != owner {
		return false, placementError("the final report has no retained actual marker creator")
	}
	created.mu.Lock()
	defer created.mu.Unlock()
	if created.closed {
		return false, placementError("the actual final marker creator is closed")
	}
	if !created.sealed.Known() {
		return false, nil
	}
	return true, created.checkLockedV2(owner.ctx)
}
