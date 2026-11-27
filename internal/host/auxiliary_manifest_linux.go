//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// writeReservationV1 is called only by this real original native owner inside
// its SAME placement transaction. It writes ONLY the dedicated auxiliary leaf
// through the original OP root; ordinary Run/Life/public state stay unchanged.
func (owner *auxiliaryOperationV1) writeReservationV1(ctx context.Context, expected, next *AuxiliaryReservationV1) error {
	if owner == nil || owner.operation == nil || owner.operation.native == nil || owner.operation.native.borrowed || owner.vacancy == nil || owner.artifacts == nil {
		return auxiliaryUnavailableV1("the auxiliary writer has no original captured native owner")
	}
	if next != nil {
		if err := validateAuxiliaryReservationV1Data(next); err != nil {
			return err
		}
	}
	operation := owner.operation
	return operation.withCurrentRead(ctx, func(owned context.Context) (failure error) {
		original, err := cachefs.OpenFile(operation.native.root, "manifest.json", os.O_RDONLY, 0600)
		if err != nil {
			return err
		}
		defer func() { failure = errors.Join(failure, original.Close()) }()
		reference, err := cachefs.CaptureFile(operation.native.root, "manifest.json", original, 0600)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: owned, reader: original}, int64(proto.MaxFrame)+1))
		if err != nil {
			return err
		}
		if len(raw) > proto.MaxFrame {
			return auxiliaryUnavailableV1("the original auxiliary manifest exceeds its protected bound")
		}
		var snapshot Manifest
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		selectedRuntime, err := startupRuntimeBytes(&snapshot)
		if err != nil {
			return err
		}
		if snapshot.ID != operation.Primary() || snapshot.SandboxOf != "" || snapshot.path != "" || !bytes.Equal(snapshot.VMNeedV1, owner.vacancy.need) || !bytes.Equal(selectedRuntime, owner.vacancy.runtime) || !reflect.DeepEqual(snapshot.Auxiliary, expected) {
			return auxiliaryUnavailableV1("the original auxiliary publication source or token changed")
		}
		snapshot.Auxiliary = cloneAuxiliaryReservationV1(next)
		encoded, err := snapshot.MarshalJSON()
		if err != nil {
			return err
		}
		if len(encoded) > proto.MaxFrame-1 {
			return auxiliaryUnavailableV1("the auxiliary accounting leaf exceeds the existing protected manifest bound")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		name := ".auxiliary-" + hex.EncodeToString(nonce[:])
		file, err := operation.native.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		published := false
		defer func() {
			if !published {
				failure = errors.Join(failure, operation.native.root.Remove(name))
			}
		}()
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			return errors.Join(err, file.Close())
		}
		if err := file.Sync(); err != nil {
			return errors.Join(err, file.Close())
		}
		prepared, err := cachefs.CaptureFile(operation.native.root, name, file, 0600)
		if err != nil {
			return errors.Join(err, file.Close())
		}
		if err := closeManifestTemp(file); err != nil {
			return errors.Join(err, file.Close())
		}
		if err := errors.Join(reference.CheckFile(operation.native.root, "manifest.json", original, 0600), operation.native.check(owned), owned.Err()); err != nil {
			return err
		}
		if err := operation.native.root.Rename(name, "manifest.json"); err != nil {
			return err
		}
		published = true
		owner.mu.Lock()
		owner.published = next != nil
		owner.mu.Unlock()
		owner.vacancy.mu.Lock()
		owner.vacancy.reservation = cloneAuxiliaryReservationV1(next)
		owner.vacancy.mu.Unlock()
		if err := syncCapturedDirectory(operation.native.root); err != nil {
			return err
		}
		current, err := cachefs.OpenFile(operation.native.root, "manifest.json", os.O_RDONLY, 0600)
		if err != nil {
			return err
		}
		defer func() { failure = errors.Join(failure, current.Close()) }()
		return errors.Join(prepared.CheckFile(operation.native.root, "manifest.json", current, 0600), operation.native.check(owned), owned.Err())
	})
}
