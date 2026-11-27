//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
)

type slotSnapshotStoppedV2 struct {
	mu              sync.Mutex
	issuer          *slotSnapshotStoppedV2
	owner           *SlotSnapshotOwnerV1
	operation       *SessionOperation
	stopped         *StoppedSessionV1
	lifetime        context.Context
	cancel          context.CancelFunc
	done            func()
	prepared        *slotSnapshotPreparedV2
	closing, closed bool
}

// checkV2 is called under the actual outside owner's operations mutex.
func (backend *slotSnapshotStoppedV2) checkV2(ctx context.Context, prepared bool) error {
	if backend == nil || backend.owner == nil || backend.issuer != backend || ctx == nil {
		return rewindFailureV1("the stopped slot has no actual backend owner")
	}
	backend.mu.Lock()
	closing := backend.closing || backend.closed
	backend.mu.Unlock()
	if closing || backend.owner.backend != backend || backend.lifetime == nil || backend.lifetime.Err() != nil {
		return rewindFailureV1("the actual stopped slot backend ended")
	}
	if !prepared && backend.prepared != nil && !backend.prepared.settledV2() {
		return rewindFailureV1("the actual prepared slot replacement must settle first")
	}
	return backend.owner.checkStoppedV2(ctx, backend.operation, backend.stopped)
}

func (backend *slotSnapshotStoppedV2) callV2(ctx context.Context, roles []*os.File, request slotlaunch.CheckpointOperationV1, use func([]*os.File) error) (slotlaunch.Response, error) {
	if err := backend.checkV2(ctx, true); err != nil {
		return slotlaunch.Response{}, err
	}
	owner := backend.owner
	if len(owner.pending) != 0 {
		return slotlaunch.Response{}, rewindFailureV1("the actual previous root operation still owns uncertain cleanup")
	}
	borrowed, cancel := context.WithCancel(ctx)
	joined := make(chan struct{})
	stop := context.AfterFunc(backend.lifetime, func() { defer close(joined); cancel() })
	ownerJoined := make(chan struct{})
	stopOwner := context.AfterFunc(owner.lifetime, func() { defer close(ownerJoined); cancel() })
	defer func() {
		cancel()
		if !stop() {
			<-joined
		}
		if !stopOwner() {
			<-ownerJoined
		}
	}()
	operation, err := slotlaunch.RunReservedCheckpointOperationV2(borrowed, roles, request)
	if operation == nil {
		return slotlaunch.Response{}, err
	}
	owner.pending = append(owner.pending, operation)
	result := operation.ResultV1()
	if use != nil {
		err = errors.Join(err, operation.WithFilesV1(borrowed, use))
	}
	closeErr := operation.Close()
	if closeErr == nil {
		owner.pending = owner.pending[:len(owner.pending)-1]
	}
	return result, errors.Join(err, closeErr, backend.checkV2(ctx, true))
}

func (backend *slotSnapshotStoppedV2) rolesV2() []*os.File {
	return append(append([]*os.File(nil), backend.owner.roles...), backend.owner.lease)
}

func (backend *slotSnapshotStoppedV2) simpleV2(ctx context.Context, request slotlaunch.CheckpointOperationV1) (slotlaunch.Response, error) {
	if backend == nil || backend.owner == nil {
		return slotlaunch.Response{}, rewindFailureV1("the actual stopped slot backend is absent")
	}
	backend.owner.operations.Lock()
	defer backend.owner.operations.Unlock()
	if err := backend.checkV2(ctx, false); err != nil {
		return slotlaunch.Response{}, err
	}
	request.Session = backend.operation.Primary()
	return backend.callV2(ctx, backend.rolesV2(), request, nil)
}

func (backend *slotSnapshotStoppedV2) Probe(ctx context.Context) error {
	_, err := backend.simpleV2(ctx, slotlaunch.CheckpointOperationV1{Action: "probe"})
	return err
}
func (backend *slotSnapshotStoppedV2) CheckSpaceV1(ctx context.Context, needed int64) error {
	if backend == nil || backend.owner == nil || needed != backend.owner.need.DiskMax {
		return rewindFailureV1("the stopped slot space check changed its original disk length")
	}
	_, err := backend.simpleV2(ctx, slotlaunch.CheckpointOperationV1{Action: "space", Needed: needed})
	return err
}
func (backend *slotSnapshotStoppedV2) Trim(ctx context.Context, keep int) error {
	_, err := backend.simpleV2(ctx, slotlaunch.CheckpointOperationV1{Action: "trim", KeepCount: keep})
	return err
}
func (backend *slotSnapshotStoppedV2) Create(ctx context.Context, at time.Time, metadata checkpoint.Metadata, keep int) (checkpoint.Entry, error) {
	if at.IsZero() {
		return checkpoint.Entry{}, rewindFailureV1("the stopped checkpoint has no actual safety time")
	}
	raw, err := checkpoint.EncodeMetadataV1(metadata)
	if err != nil {
		return checkpoint.Entry{}, err
	}
	name := at.UTC().Format(checkpoint.TimeLayout)
	_, err = backend.simpleV2(ctx, slotlaunch.CheckpointOperationV1{Action: "create", Name: name, Metadata: raw, KeepCount: keep})
	if err != nil {
		return checkpoint.Entry{}, err
	}
	copy, err := checkpoint.DecodeMetadataV1(raw)
	when, parseErr := time.Parse(checkpoint.TimeLayout, name)
	return checkpoint.Entry{Time: when, Metadata: copy}, errors.Join(err, parseErr)
}
func (backend *slotSnapshotStoppedV2) List(ctx context.Context) ([]checkpoint.Entry, error) {
	response, err := backend.simpleV2(ctx, slotlaunch.CheckpointOperationV1{Action: "list"})
	if err != nil {
		return nil, err
	}
	if len(response.Checkpoints) > 1024 {
		return nil, rewindFailureV1("the native checkpoint list exceeds its bound")
	}
	var entries []checkpoint.Entry
	for _, name := range response.Checkpoints {
		selected, err := backend.Select(ctx, name)
		if err != nil {
			return nil, errors.Join(err, backend.closeSelectedV2(selected))
		}
		entries = append(entries, selected.Entry())
		if err := backend.closeSelectedV2(selected); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (backend *slotSnapshotStoppedV2) Select(ctx context.Context, name string) (checkpoint.SnapshotV1, error) {
	if backend == nil || backend.owner == nil {
		return nil, rewindFailureV1("the stopped slot Snapshot source is absent")
	}
	owner := backend.owner
	owner.operations.Lock()
	defer owner.operations.Unlock()
	if err := backend.checkV2(ctx, false); err != nil {
		return nil, err
	}
	when, err := time.Parse(checkpoint.TimeLayout, name)
	if err != nil || when.Format(checkpoint.TimeLayout) != name {
		return nil, rewindFailureV1("the stopped selection changed its fixed timestamp")
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	selected := &slotSnapshotReadV1{origin: owner, lifetime: lifetime, cancel: cancel}
	selected.issuer = selected
	owner.incomplete = append(owner.incomplete, selected)
	_, err = backend.callV2(ctx, backend.rolesV2(), slotlaunch.CheckpointOperationV1{Session: backend.operation.Primary(), Action: "select", Name: name}, func(files []*os.File) error {
		if len(files) != 2 {
			return rewindFailureV1("the actual stopped selection returned another read vector")
		}
		for index, file := range files {
			duplicate, dupErr := duplicateSlotSnapshotFileV1(file)
			if duplicate != nil {
				if index == 0 {
					selected.disk = duplicate
				} else {
					selected.metadata = duplicate
				}
			}
			if dupErr != nil {
				return dupErr
			}
		}
		return nil
	})
	if selected.disk == nil || selected.metadata == nil {
		return selected, errors.Join(err, rewindFailureV1("the actual stopped selection retained incomplete read caps"))
	}
	for _, file := range owner.roles {
		duplicate, dupErr := duplicateSlotSnapshotFileV1(file)
		if duplicate != nil {
			selected.roles = append(selected.roles, duplicate)
		}
		if dupErr != nil {
			return selected, errors.Join(err, dupErr)
		}
	}
	if readErr := selected.captureReadFilesV1(ctx, when, owner.need.DiskMax); readErr != nil {
		return selected, errors.Join(err, readErr)
	}
	selected.admitted = true
	for index, pending := range owner.incomplete {
		if pending == selected {
			owner.incomplete = append(owner.incomplete[:index], owner.incomplete[index+1:]...)
			break
		}
	}
	return selected, err
}

func (backend *slotSnapshotStoppedV2) Close() error {
	if backend == nil {
		return nil
	}
	backend.mu.Lock()
	if backend.issuer != backend || backend.owner == nil {
		backend.mu.Unlock()
		return rewindFailureV1("a copied stopped backend cannot release the original source")
	}
	if backend.closed {
		backend.mu.Unlock()
		return nil
	}
	backend.mu.Unlock()
	owner := backend.owner
	owner.operations.Lock()
	defer owner.operations.Unlock()
	// A refused Close must leave the same prepared cleanup callable. Do not
	// cancel its execution lifetime or set closing before this guard succeeds.
	if backend.prepared != nil && !backend.prepared.closed {
		return rewindFailureV1("the actual prepared slot owner must settle and close before its backend")
	}
	if len(owner.pending) != 0 {
		return rewindFailureV1("the actual root operation must finish cleanup before the stopped source releases")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil
	}
	if owner.backend != backend {
		return rewindFailureV1("the actual stopped backend changed its original outside owner")
	}
	backend.closing = true
	backend.cancel()
	owner.backend = nil
	backend.closed = true
	backend.done()
	return nil
}

var _ checkpoint.BackendV1 = (*slotSnapshotStoppedV2)(nil)

func (backend *slotSnapshotStoppedV2) closeSelectedV2(selected checkpoint.SnapshotV1) error {
	if selected == nil {
		return nil
	}
	err := selected.Close()
	if err != nil {
		if actual, ok := selected.(*slotSnapshotReadV1); ok {
			backend.owner.operations.Lock()
			backend.owner.incomplete = append(backend.owner.incomplete, actual)
			backend.owner.operations.Unlock()
		}
	}
	return err
}
