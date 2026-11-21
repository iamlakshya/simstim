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
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// These pins retain the exact canonical bytes, not a reopened DTO source.
// Only the actual owning restart writer may advance the pin to its own CAS.
func (cold *NoneColdResumeV1) captureMetadataV1(ctx context.Context, expected []byte) (returned error) {
	file, err := cachefs.OpenFile(cold.operation.native.root, "manifest.json", os.O_RDONLY, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, file.Close())
		}
	}()
	ref, err := cachefs.CaptureFile(cold.operation.native.root, "manifest.json", file, 0600)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: ctx, reader: file}, int64(proto.MaxFrame)+1))
	if err != nil {
		return err
	}
	if len(raw) > proto.MaxFrame {
		return sessionLeaseError("the original None metadata exceeds its protected bound")
	}
	if expected != nil && !bytes.Equal(raw, expected) {
		return sessionLeaseError("the actual None restart publication differs from its canonical writer")
	}
	if expected == nil {
		var decoded Manifest
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return err
		}
		actual, err := decoded.MarshalJSON()
		if err != nil {
			return err
		}
		original, err := cold.source.MarshalJSON()
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, original) {
			return sessionLeaseError("the original None cleanup changed before its canonical pin")
		}
	}
	if err := errors.Join(ref.CheckFile(cold.operation.native.root, "manifest.json", file, 0600), cold.operation.native.check(ctx), ctx.Err()); err != nil {
		return err
	}
	if cold.metadata != nil {
		if err := cold.metadata.Close(); err != nil {
			return err
		}
	}
	cold.metadata, cold.metadataRef, cold.metadataBytes = file, ref, bytes.Clone(raw)
	complete = true
	return nil
}

func (cold *NoneColdResumeV1) checkMetadataV1(ctx context.Context) error {
	if cold.metadata == nil || len(cold.metadataBytes) == 0 {
		return sessionLeaseError("the original None continuation lost its canonical byte pin")
	}
	if err := cold.metadataRef.CheckFile(cold.operation.native.root, "manifest.json", cold.metadata, 0600); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: ctx, reader: io.NewSectionReader(cold.metadata, 0, int64(proto.MaxFrame)+1)}, int64(proto.MaxFrame)+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, cold.metadataBytes) {
		return sessionLeaseError("the original None canonical run changed during its continuation")
	}
	return errors.Join(cold.metadataRef.CheckFile(cold.operation.native.root, "manifest.json", cold.metadata, 0600), ctx.Err())
}

func checkNoneColdRestartWriterV1(ctx context.Context, operation *SessionOperation, before, next *Manifest) error {
	if operation == nil || operation.noneCold == nil {
		return nil
	}
	cold := operation.noneCold
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if !cold.bound || cold.published || cold.handed || cold.closed || before == nil || next == nil || !noneColdRoleV1(before) || !noneColdRoleV1(next) || !reflect.DeepEqual(next.Wall, cold.source.Wall) || next.NoneCleanup != nil || next.Run.BootID != cold.boot || next.Status.State != StateStarting {
		return sessionLeaseError("the original None restart writer changed its exact role or cleanup epoch")
	}
	if err := cold.checkOriginV1(ctx); err != nil {
		return err
	}
	if err := cold.checkBeforeNativeV1(ctx); err != nil {
		return err
	}
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return err
	}
	next.Run.Generation = hex.EncodeToString(epoch[:])
	next.NoneDirectoryCreation = nil
	next.Life = &Life{Phase: LifeBooting, BootBy: time.Now().UTC().Add(time.Minute)}
	return nil
}

// Cancellation before any real native dispatch restores only this SAME
// original source's saved bytes, under the actual OP/holder and a current CAS.
// An attempted or unknown creation never reaches this rollback.
func (cold *NoneColdResumeV1) restoreUnstartedMetadataV1() (returned error) {
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if cold.closed || cold.handed || !cold.bound || !cold.published {
		return nil
	}
	if cold.self != cold || cold.operation == nil || !cold.operation.Valid(cold.operation.Primary()) || cold.holder == nil && cold.managed == nil || len(cold.originalBytes) == 0 {
		return sessionLeaseError("the cancelled None stage lost its original owner")
	}
	launch := cold.operation.startup
	if cold.managed != nil {
		if !cold.managed.cancelled || cold.managed.claimed {
			return sessionLeaseError("the managed None stage has no actual unclaimed native settlement")
		}
	} else if launch != nil && (launch.started || launch.unit != nil || launch.child != nil) {
		return sessionLeaseError("the None cancellation cannot restore data after a possible native creation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return withPlacement(ctx, true, func(control *placementControl) (failure error) {
		if err := errors.Join(CheckCaller(), ctx.Err(), cold.operation.native.check(ctx), cold.checkRetainedHolderV1(ctx), cold.checkMetadataV1(ctx)); err != nil {
			return err
		}
		if BootID() != cold.boot {
			return sessionLeaseError("the kernel boot changed before cancelling the None stage")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		name := ".none-cancel-" + hex.EncodeToString(nonce[:])
		file, err := cold.operation.native.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		published := false
		defer func() {
			if !published {
				failure = errors.Join(failure, cold.operation.native.root.Remove(name))
			}
		}()
		if _, err := file.Write(cold.originalBytes); err != nil {
			return errors.Join(err, file.Close())
		}
		if err := file.Sync(); err != nil {
			return errors.Join(err, file.Close())
		}
		ref, err := cachefs.CaptureFile(cold.operation.native.root, name, file, 0600)
		if err != nil {
			return errors.Join(err, file.Close())
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := errors.Join(cold.checkMetadataV1(ctx), cold.checkRetainedHolderV1(ctx), cold.operation.native.check(ctx), ctx.Err()); err != nil {
			return err
		}
		if err := cold.operation.native.root.Rename(name, "manifest.json"); err != nil {
			return err
		}
		published = true
		if err := cold.captureMetadataV1(ctx, cold.originalBytes); err != nil {
			return err
		}
		if err := ref.CheckFile(cold.operation.native.root, "manifest.json", cold.metadata, 0600); err != nil {
			return err
		}
		if err := syncCapturedDirectory(cold.operation.native.root); err != nil {
			return err
		}
		cold.published = false
		return nil
	}, false)
}

func retainNoneColdRestartWriterV1(ctx context.Context, operation *SessionOperation, next *Manifest) error {
	if operation == nil || operation.noneCold == nil {
		return nil
	}
	cold := operation.noneCold
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if !cold.bound || cold.published || cold.handed || cold.closed || next == nil || !noneColdRoleV1(next) || next.NoneCleanup != nil || next.Run.BootID != cold.boot || next.Status.State != StateStarting {
		return sessionLeaseError("the original None restart publication lost its writer")
	}
	encoded, err := next.MarshalJSON()
	if err != nil {
		return err
	}
	if err := cold.captureMetadataV1(ctx, append(encoded, '\n')); err != nil {
		return err
	}
	cold.published = true
	return ctx.Err()
}

// A duplicate is kept only for the exact created lifetime. Its descriptor
// cannot be replaced by a process number during the eventual code/OP takeover.
func (cold *NoneColdResumeV1) checkRetainedCreationV1() error {
	if cold.creation == nil {
		return sessionLeaseError("the original None creation handle is absent")
	}
	polls := []unix.PollFd{{Fd: int32(cold.creation.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(polls, 0)
	if err != nil || n != 0 {
		return errors.Join(sessionLeaseError("the original newly created None supervisor ended before takeover"), err)
	}
	return nil
}
