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
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// saveRestartManifestV1 publishes through the original retained primary root.
// The same canonical decoder and one MarshalJSON preserve opaque metadata.
// Original Need/runtime/driver identity stay unchanged. A changed memory cap
// consumes only the actual request admission retained on this SAME native OP.
func saveRestartManifestV1(operation *SessionOperation, next *Manifest) error {
	if operation == nil || operation.native == nil || operation.native.borrowed || next == nil || next.ID != operation.Primary() ||
		next.path != "" || next.SandboxOf != "" || !operation.Valid(next.ID) {
		return sessionLeaseError("the restart writer has no original owning primary operation")
	}
	ctx := operation.ownerContext
	if ctx == nil {
		return sessionLeaseError("the restart writer has no original request lifetime")
	}
	if err := CheckCaller(); err != nil {
		return err
	}
	return operation.withCurrentRead(ctx, func(owned context.Context) error {
		return withPlacement(owned, false, func(_ *placementControl) (failure error) {
			original, err := cachefs.OpenFile(operation.native.root, "manifest.json", os.O_RDONLY, 0o600)
			if err != nil {
				return err
			}
			defer func() { failure = errors.Join(failure, original.Close()) }()
			reference, err := cachefs.CaptureFile(operation.native.root, "manifest.json", original, 0o600)
			if err != nil {
				return err
			}
			raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: owned, reader: original}, int64(proto.MaxFrame)+1))
			if err != nil {
				return err
			}
			if len(raw) > proto.MaxFrame {
				return memberError("the original restart manifest exceeds its protected bound")
			}
			var before Manifest
			if err := json.Unmarshal(raw, &before); err != nil {
				return err
			}
			if before.ID != operation.Primary() || before.SandboxOf != "" || before.path != "" || !bytes.Equal(before.VMNeedV1, next.VMNeedV1) ||
				!reflect.DeepEqual(before.RuntimeManifest, next.RuntimeManifest) || wallDriver(&before) != wallDriver(next) || wallLevel(&before) != wallLevel(next) {
				return sessionLeaseError("the prospective restart changed its original protected execution selection")
			}
			if err := reference.CheckFile(operation.native.root, "manifest.json", original, 0o600); err != nil {
				return err
			}
			if err := consumeRestartMemoryV1(operation, &before, next); err != nil {
				return err
			}
			if err := ValidateManifestStart(next); err != nil {
				return err
			}
			if len(next.AutomaticRestore) != 0 {
				if _, err := ValidateAutomaticRestore(next); err != nil {
					return err
				}
			}
			next.UpdatedAt = time.Now().UTC()
			encoded, err := next.MarshalJSON()
			if err != nil {
				return err
			}
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return err
			}
			name := ".restart-" + hex.EncodeToString(nonce[:])
			prepared, err := operation.native.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			published := false
			defer func() {
				if !published {
					failure = errors.Join(failure, operation.native.root.Remove(name))
				}
			}()
			if _, err := prepared.Write(append(encoded, '\n')); err != nil {
				return errors.Join(err, prepared.Close())
			}
			if err := prepared.Sync(); err != nil {
				return errors.Join(err, prepared.Close())
			}
			after, err := cachefs.CaptureFile(operation.native.root, name, prepared, 0o600)
			if err != nil {
				return errors.Join(err, prepared.Close())
			}
			if err := closeManifestTemp(prepared); err != nil {
				return errors.Join(err, prepared.Close())
			}
			if err := errors.Join(reference.CheckFile(operation.native.root, "manifest.json", original, 0o600), operation.native.check(owned), owned.Err()); err != nil {
				return err
			}
			if err := operation.native.root.Rename(name, "manifest.json"); err != nil {
				return err
			}
			published = true
			if err := syncCapturedDirectory(operation.native.root); err != nil {
				return err
			}
			current, err := cachefs.OpenFile(operation.native.root, "manifest.json", os.O_RDONLY, 0o600)
			if err != nil {
				return err
			}
			defer func() { failure = errors.Join(failure, current.Close()) }()
			if err := after.CheckFile(operation.native.root, "manifest.json", current, 0o600); err != nil {
				return err
			}
			return operation.native.check(owned)
		}, false)
	})
}
