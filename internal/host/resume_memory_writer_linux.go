//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"io"
	"os"
	"reflect"
)

// This dedicated writer changes ONLY the original pending leaf through its
// SAME OP root, inside the already-held native placement transaction.
func (lease *resumePreparationLeaseV1) writeLeafLockedV1(ctx context.Context, expected, next *ResumePreparationV1) error {
	if lease == nil || lease.operation == nil || lease.operation.native == nil || lease.operation.native.borrowed {
		return resumePreparationDataErrorV1()
	}
	if err := validateResumePreparationV1Data(next); err != nil {
		return err
	}
	operation := lease.operation
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
			return resumePreparationErrorV1("the original preparation manifest exceeds its protected bound")
		}
		var snapshot Manifest
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		selectedRuntime, err := startupRuntimeBytes(&snapshot)
		if err != nil {
			return err
		}
		if snapshot.ID != operation.Primary() || snapshot.SandboxOf != "" || snapshot.path != "" || !bytes.Equal(snapshot.VMNeedV1, lease.need) || !bytes.Equal(selectedRuntime, lease.runtime) || !reflect.DeepEqual(snapshot.ResumePreparation, expected) {
			return resumePreparationErrorV1("the original preparation publication source or token changed")
		}
		snapshot.ResumePreparation = cloneResumePreparationV1(next)
		encoded, err := snapshot.MarshalJSON()
		if err != nil {
			return err
		}
		if len(encoded) > proto.MaxFrame-1 {
			return resumePreparationErrorV1("the preparation accounting leaf exceeds the existing protected manifest bound")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		name := ".resume-preparation-" + hex.EncodeToString(nonce[:])
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
		lease.published = next != nil

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
