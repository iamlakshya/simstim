package host

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
)

type noneDeleteInvocationV1 struct {
	self      *noneDeleteInvocationV1
	operation *SessionOperation
	active    atomic.Bool
}

func (invocation *noneDeleteInvocationV1) current(operation *SessionOperation) bool {
	return invocation != nil && invocation.self == invocation && invocation.operation == operation && operation != nil && operation.active.Load() && invocation.active.Load()
}

// Exactly two concrete None-only inputs can feed metadata removal. This is
// neither a caller-supplied checker nor an ordinary/walled stopped interface.
type noneDeleteSourceV1 struct {
	self                *noneDeleteSourceV1
	operation           *SessionOperation
	live                *StoppedNoneSessionV1
	stored              *noneStorageSubjectV1
	directory           *noneDirectoryCleanupV1
	metadata            *noneMetadataCleanupV1
	metadataDiagnostics error
}

func (source *noneDeleteSourceV1) Check(ctx context.Context, operation *SessionOperation) error {
	if source == nil || source.self != source || source.operation != operation || (source.live == nil) == (source.stored == nil) {
		return sessionLeaseError("the actual None deletion has no exact concrete subject")
	}
	if source.metadata != nil && source.metadata.started {
		return source.checkNativeAfterRemovalV1(ctx, operation)
	}
	if source.live != nil {
		return source.live.Check(ctx, operation)
	}
	return source.stored.Check(ctx, operation)
}
func (source *noneDeleteSourceV1) checkNativeAfterRemovalV1(ctx context.Context, operation *SessionOperation) error {
	if source == nil || source.self != source || source.operation != operation || (source.live == nil) == (source.stored == nil) {
		return sessionLeaseError("the original None deletion completion is absent")
	}
	if source.live != nil {
		return source.live.checkNativeAfterRemovalV1(ctx, operation)
	}
	return source.stored.checkNativeAfterRemovalV1(ctx, operation)
}
func (source *noneDeleteSourceV1) Close() error {
	if source == nil {
		return nil
	}
	if source.self != source {
		return sessionLeaseError("a copied None deletion cannot release its original subject")
	}
	if source.directory.mutationStartedV1() && (source.metadata == nil || !source.metadata.complete) {
		var closeErr error
		if source.metadata != nil && !source.metadata.ready {
			closeErr = source.retireIncompleteMetadataV1()
		}
		return errors.Join(noneDirectoryOriginErrorV1("the SAME original None Delete operation remains retained after directory mutation and incomplete metadata cleanup"), closeErr)
	}
	if source.metadata != nil {
		if err := source.metadata.Close(); err != nil {
			return err
		}
	}
	if source.directory != nil {
		if err := source.directory.Close(); err != nil {
			return err
		}
	}
	if source.live != nil {
		return source.live.Close()
	}
	if source.stored != nil {
		return source.stored.Close()
	}
	return nil
}

// A primary none session uses its distinct actual N5 stop producer before the
// existing metadata/worktree deletion rules. It never acquires a second OP.
type noneDeleteRetainedV1 struct {
	mu         sync.Mutex
	invocation *noneDeleteInvocationV1
	operation  *SessionOperation
	subject    *noneDeleteSourceV1
	current    *Manifest
	children   []*Manifest
}

func checkRetainedNoneMembersV1(children []*Manifest, expected *[]string) error {
	if expected == nil {
		if len(children) != 0 {
			return UserError("upgrade_required", "the primary has other sandbox sessions", "Use a current client to list and confirm every session before deleting this sandbox.")
		}
		return nil
	}
	ids := make([]string, 0, len(children))
	for _, child := range children {
		ids = append(ids, child.ID)
	}
	slices.Sort(ids)
	want := slices.Clone(*expected)
	slices.Sort(want)
	for i, id := range want {
		if !api.ValidSessionID(id) || i > 0 && want[i-1] == id {
			return memberError("the confirmed sandbox member list is invalid")
		}
	}
	if !slices.Equal(ids, want) {
		return UserError("sandbox_changed", "this retry changed the original selected sandbox members", "Retry the original confirmed member set without adding or removing sessions.")
	}
	return nil
}

func deleteNonePrimaryV1(id string, name *string, members *[]string, result *DeleteResult) (returned error) {
	retained, err := noneCustodyV1.admitDeleteV1(id)
	if err != nil {
		return err
	}
	defer retained.mu.Unlock()
	defer func() {
		if retained.subject != nil {
			closeErr := retained.subject.Close()
			returned = errors.Join(returned, retained.subject.closeDiagnosticsV1())
			if err := closeErr; err != nil {
				returned = errors.Join(returned, err)
				return
			}
		}
		if retained.operation != nil {
			if err := retained.operation.CloseWithError(); err != nil {
				returned = errors.Join(returned, err)
				return
			}
		}
		if retained.invocation != nil {
			retained.invocation.active.Store(false)
		}
		noneCustodyV1.finishDeleteV1(id, retained)
	}()
	if retained.operation == nil {
		operation, err := BeginSessionOperation(context.Background(), id, 0)
		if err != nil {
			return err
		}
		invocation := &noneDeleteInvocationV1{operation: operation}
		invocation.self = invocation
		invocation.active.Store(true)
		retained.operation, retained.invocation = operation, invocation
	}
	operation := retained.operation
	if retained.current == nil {
		current, err := operation.Load()
		if err != nil {
			return err
		}
		if !NoneSupervisorShapeV1(current) {
			return sessionLeaseError("the original unwalled deletion source changed")
		}
		if name != nil && (renameUnsaved(id, current) || current.Name != *name) {
			return UserError("session_renamed", "the unwalled session's name changed before deletion", "List the host's sessions and select the session again.")
		}
		children, err := checkDeleteMembers(current, members)
		if err != nil {
			return err
		}
		if afterNameCheck != nil {
			afterNameCheck(id)
		}
		subject := &noneDeleteSourceV1{operation: operation}
		subject.self = subject
		// Adopt before construction: an error may return its SAME partial stop.
		retained.subject, retained.current, retained.children = subject, current, children
		if current.Run.BootID != BootID() || validNoneCleanupV1(current, BootID()) {
			subject.stored, err = beginNoneStoredDeleteV1(operation.ownerContext, operation, retained.invocation)
		} else {
			subject.live, err = StopNoneSessionForOperationV1(operation.ownerContext, operation)
		}
		if err != nil {
			return err
		}
	} else {
		if name != nil && retained.current.Name != *name {
			return UserError("session_renamed", "this retry selected a different None session name", "Retry the original deletion without changing its selected session.")
		}
		if err := checkRetainedNoneMembersV1(retained.children, members); err != nil {
			return err
		}
	}
	if err := retained.subject.Check(context.Background(), operation); err != nil {
		return err
	}
	if err := retained.subject.cleanupOriginalDirectoryV1(operation.ownerContext); err != nil {
		return err
	}
	return deleteNoneMetadataV1(operation, retained.subject, retained.current, retained.children, result)
}

func deleteNoneMetadataV1(operation *SessionOperation, source *noneDeleteSourceV1, current *Manifest, children []*Manifest, result *DeleteResult) error {
	if source.metadata != nil && !source.metadata.ready {
		if err := source.retireIncompleteMetadataV1(); err != nil {
			return err
		}
	}
	if source.metadata == nil {
		plan, err := captureNoneMetadataCleanupV1(operation, source, current, children)
		if plan != nil {
			source.metadata = plan
		}
		if err != nil {
			return err
		}
	}
	return source.metadata.Remove(context.Background(), result)
}

func (source *noneDeleteSourceV1) retireIncompleteMetadataV1() error {
	if err := source.metadata.Close(); err != nil {
		return err
	}
	source.metadataDiagnostics = errors.Join(source.metadataDiagnostics, source.metadata.closeDiagnosticsV1())
	source.metadata = nil
	return nil
}

func (source *noneDeleteSourceV1) closeDiagnosticsV1() error {
	return errors.Join(source.metadataDiagnostics, source.metadata.closeDiagnosticsV1(), source.directory.closeDiagnosticsV1(), source.stored.CloseDiagnosticsV4(), source.live.CloseDiagnosticsV4())
}
