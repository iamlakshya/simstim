//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This distinct subject can only remove this actual Delete's None storage.
// It cannot issue a runtime, restart, ordinary/walled stopped or VM subject.
type noneStorageSubjectV1 struct {
	mu                         sync.Mutex
	self                       *noneStorageSubjectV1
	invocation                 *noneDeleteInvocationV1
	operation                  *SessionOperation
	holder                     *sessionFileLease
	manager                    *nativeUserManagerV1
	source                     Manifest
	boot                       string
	members                    []string
	metadata                   *os.File
	reference                  cachefs.Reference
	raw                        []byte
	opIdentity, holderIdentity os.FileInfo
	closed                     bool
	managerDiagnostic          error
}

func beginNoneStoredDeleteV1(ctx context.Context, operation *SessionOperation, invocation *noneDeleteInvocationV1) (result *noneStorageSubjectV1, returned error) {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || !invocation.current(operation) {
		return nil, sessionLeaseError("the None storage subject has no actual outside Delete operation")
	}
	if err := errors.Join(CheckCaller(), ctx.Err()); err != nil {
		return nil, err
	}
	file, err := cachefs.OpenFile(operation.native.root, "manifest.json", os.O_RDONLY, 0600)
	if err != nil {
		return nil, err
	}
	subject := &noneStorageSubjectV1{invocation: invocation, operation: operation, metadata: file, boot: BootID()}
	subject.self = subject
	complete := false
	defer func() {
		if !complete {
			closeErr := subject.Close()
			returned = errors.Join(returned, subject.CloseDiagnosticsV4())
			if closeErr != nil {
				result = subject
				returned = errors.Join(returned, closeErr)
			}
		}
	}()
	subject.reference, err = cachefs.CaptureFile(operation.native.root, "manifest.json", file, 0600)
	if err != nil {
		return nil, err
	}
	subject.raw, err = io.ReadAll(io.LimitReader(sessionOperationReader{ctx: ctx, reader: file}, int64(proto.MaxFrame)+1))
	if err != nil {
		return nil, err
	}
	if len(subject.raw) > proto.MaxFrame || json.Unmarshal(subject.raw, &subject.source) != nil || subject.source.ID != operation.Primary() || !NoneSupervisorShapeV1(&subject.source) || !auxiliaryBootIDV1(subject.boot) || !auxiliaryBootIDV1(subject.source.Run.BootID) {
		return nil, noneDirectoryOriginErrorV1("the Delete operation does not select an exact supported None storage epoch")
	}
	if subject.source.Run.BootID == subject.boot && !validNoneCleanupV1(&subject.source, subject.boot) {
		return nil, noneDirectoryOriginErrorV1("the original current-boot None run did not confirm complete cleanup")
	}
	subject.holder, err = openNoneColdHolderV1(ctx, operation, &subject.source)
	if err != nil {
		return nil, err
	}
	subject.opIdentity, err = operation.native.file.Stat()
	if err != nil {
		return nil, err
	}
	subject.holderIdentity, err = subject.holder.file.Stat()
	if err != nil {
		return nil, err
	}
	subject.manager, err = openNativeUserManagerV1(ctx)
	if err != nil && (subject.manager != nil || !errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	subject.members, err = captureNoneColdMemberIDsV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	if err := subject.Check(ctx, operation); err != nil {
		return nil, err
	}
	complete = true
	return subject, nil
}

func (subject *noneStorageSubjectV1) checkActorAbsenceV1(ctx context.Context) error {
	if subject.source.Run.BootID == subject.boot {
		if err := checkNoneColdActorsV1(ctx, &subject.source, subject.members...); err != nil {
			return err
		}
	}
	if subject.manager != nil {
		if err := subject.manager.requireAuxiliaryUnitAbsentV1(ctx, UnitName(subject.source.ID)); err != nil {
			return err
		}
		aux, err := AuxUnitNameV1(subject.source.ID)
		if err != nil {
			return err
		}
		return subject.manager.requireAuxiliaryUnitAbsentV1(ctx, aux)
	}
	_, manager, err := CgroupNamespaces()
	if err != nil {
		return err
	}
	if manager != "" || subject.source.Run.BootID == subject.boot && subject.source.Run.Unit != "" {
		return sessionLeaseError("the original None storage epoch has no actual matching managerless placement")
	}
	return ctx.Err()
}

func (subject *noneStorageSubjectV1) Check(ctx context.Context, operation *SessionOperation) error {
	if subject == nil || subject.self != subject || ctx == nil {
		return sessionLeaseError("the original None storage subject is absent or copied")
	}
	subject.mu.Lock()
	defer subject.mu.Unlock()
	if subject.closed || subject.operation != operation || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || !subject.invocation.current(operation) || subject.holder == nil || subject.metadata == nil || BootID() != subject.boot {
		return sessionLeaseError("the actual None storage operation ended or changed")
	}
	if err := errors.Join(CheckCaller(), ctx.Err(), subject.holder.check(ctx), operation.native.rootRef.CheckRoot(subject.holder.root), subject.reference.CheckFile(operation.native.root, "manifest.json", subject.metadata, 0600)); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: ctx, reader: io.NewSectionReader(subject.metadata, 0, int64(proto.MaxFrame)+1)}, int64(proto.MaxFrame)+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, subject.raw) {
		return sessionLeaseError("the original canonical None storage record changed during Delete")
	}
	if err := subject.checkActorAbsenceV1(ctx); err != nil {
		return err
	}
	return errors.Join(operation.native.check(ctx), subject.holder.check(ctx), ctx.Err())
}

// Only the actual metadata remover calls this after its own rooted unlink.
// Native SAME descriptions, not missing names, close that deletion transaction.
func (subject *noneStorageSubjectV1) checkNativeAfterRemovalV1(ctx context.Context, operation *SessionOperation) error {
	if subject == nil || subject.self != subject || ctx == nil {
		return sessionLeaseError("the original None storage completion is absent")
	}
	subject.mu.Lock()
	defer subject.mu.Unlock()
	if subject.closed || subject.operation != operation || operation == nil || !operation.active.Load() || !subject.invocation.current(operation) || operation.native == nil || operation.native.file == nil || subject.holder == nil || subject.holder.file == nil || BootID() != subject.boot {
		return sessionLeaseError("the actual original None deletion descriptions ended")
	}
	op, err := operation.native.file.Stat()
	if err != nil {
		return err
	}
	holder, err := subject.holder.file.Stat()
	if err != nil {
		return err
	}
	if subject.opIdentity == nil || subject.holderIdentity == nil || !os.SameFile(op, subject.opIdentity) || !os.SameFile(holder, subject.holderIdentity) {
		return sessionLeaseError("the original None deletion native descriptions were replaced")
	}
	return errors.Join(CheckCaller(), observeNoneHolderV1(subject.holder.file), checkBorrowedFlockV1(operation.native.file, startupCredentials{pid: os.Getpid(), uid: uint32(os.Getuid())}), subject.checkActorAbsenceV1(ctx), ctx.Err())
}

func (subject *noneStorageSubjectV1) Close() error {
	if subject == nil {
		return nil
	}
	if subject.self != subject {
		return sessionLeaseError("a copied None storage subject cannot close its original resources")
	}
	subject.mu.Lock()
	defer subject.mu.Unlock()
	if subject.closed {
		return nil
	}
	var err error
	if subject.metadata != nil {
		err = errors.Join(err, subject.metadata.Close())
	}
	if subject.holder != nil {
		err = errors.Join(err, subject.holder.Close())
	}
	if subject.manager != nil {
		err = errors.Join(err, closeNativeManagerResourceV4(subject.manager, subject.operation.Primary(), &subject.managerDiagnostic))
	}
	if err == nil {
		subject.closed = true
	}
	return err
}

func (subject *noneStorageSubjectV1) CloseDiagnosticsV4() error {
	if subject == nil {
		return nil
	}
	return subject.managerDiagnostic
}
