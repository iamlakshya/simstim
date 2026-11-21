//go:build linux

package microvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// This object is created only inside the SAME running native disk borrow.
// Its current original phase supplies native sender/disk/code checks for each
// fixed request; the host separately retains its real OP and freeze lease.
type subordinateCheckpointBackendV3 struct {
	mu         sync.Mutex
	original   *subordinateCheckpointBackendV3
	phase      *vmmExecPhaseV1
	controller *runnerControl
	closed     bool
}

func (binding *sandboxNativeBinding) hasSubordinateCheckpointV3() bool {
	if binding == nil || binding.proof == nil {
		return false
	}
	binding.proof.mu.Lock()
	defer binding.proof.mu.Unlock()
	return binding.proof.readonly != nil
}

func (binding *sandboxNativeBinding) withSubordinateCheckpointV3(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, use func(checkpoint.BackendV1) error) error {
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	binding.proof.mu.Lock()
	phase := binding.proof.readonly
	binding.proof.mu.Unlock()
	if phase == nil || !phase.isReadonlyV1() {
		return fmt.Errorf("the actual checkpoint disk has no original subordinate source")
	}
	binding.sandbox.mu.Lock()
	owner := binding.sandbox.snapshotDisk
	controller := binding.sandbox.control
	binding.sandbox.mu.Unlock()
	if owner == nil || controller == nil {
		return fmt.Errorf("the original checkpoint actor lost its native disk/control owner")
	}
	return owner.borrow(ctx, func(_ *os.Root, _ *os.Root, _ *os.File) error {
		if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
			return err
		}
		backend := &subordinateCheckpointBackendV3{phase: phase, controller: controller}
		backend.original = backend
		defer func() { backend.mu.Lock(); backend.closed = true; backend.mu.Unlock() }()
		result := use(backend)
		return errors.Join(result, binding.checkNativeBinding(ctx, connection, expected))
	})
}

func (backend *subordinateCheckpointBackendV3) callV3(ctx context.Context, request checkpointActorRequestV3) (*checkpointActorReplyV3, error) {
	if backend == nil {
		return nil, fmt.Errorf("the original checkpoint actor backend is absent")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.original != backend || backend.closed || backend.phase == nil || backend.controller == nil || ctx == nil {
		return nil, fmt.Errorf("the original checkpoint actor borrow ended or was copied")
	}
	if err := backend.phase.checkSnapshotDiskV2(ctx); err != nil {
		return nil, err
	}
	request.Version = 3
	message, err := backend.controller.callRequestV3(ctx, runnerRequest{Op: "checkpoint_v3", Checkpoint: &request})
	if err != nil {
		return nil, err
	}
	reply := message.Checkpoint
	if reply == nil || reply.Version != 3 || reply.Action != request.Action {
		return nil, fmt.Errorf("the actual actor reply changed its original fixed request")
	}
	if reply.Error != nil {
		return nil, reply.Error
	}
	if err := backend.phase.checkSnapshotDiskV2(ctx); err != nil {
		return nil, err
	}
	return reply, nil
}

func (backend *subordinateCheckpointBackendV3) Probe(ctx context.Context) error {
	_, err := backend.callV3(ctx, checkpointActorRequestV3{Action: "probe"})
	return err
}

func (backend *subordinateCheckpointBackendV3) CheckSpaceV1(ctx context.Context, needed int64) error {
	_, err := backend.callV3(ctx, checkpointActorRequestV3{Action: "space", Needed: needed})
	return err
}

func (backend *subordinateCheckpointBackendV3) List(ctx context.Context) ([]checkpoint.Entry, error) {
	var entries []checkpoint.Entry
	after := ""
	for count := 0; count <= 10000; count++ {
		reply, err := backend.callV3(ctx, checkpointActorRequestV3{Action: "list", After: after})
		if err != nil {
			return nil, err
		}
		if reply.Entry == nil {
			return entries, nil
		}
		key := reply.Entry.Time.Format(checkpoint.TimeLayout)
		if reply.Entry.Time.IsZero() || after != "" && key >= after || count == 10000 {
			return nil, fmt.Errorf("the actual checkpoint actor list exceeds its bound or changes cursor")
		}
		if _, err := checkpoint.EncodeMetadataV1(reply.Entry.Metadata); err != nil {
			return nil, err
		}
		entries, after = append(entries, *reply.Entry), key
	}
	return nil, fmt.Errorf("the checkpoint actor list did not terminate")
}

func (backend *subordinateCheckpointBackendV3) Create(ctx context.Context, at time.Time, metadata checkpoint.Metadata, keep int) (checkpoint.Entry, error) {
	expected, err := checkpoint.EncodeMetadataV1(metadata)
	if err != nil {
		return checkpoint.Entry{}, err
	}
	reply, err := backend.callV3(ctx, checkpointActorRequestV3{Action: "create", When: at, Metadata: metadata, Keep: keep})
	if err != nil {
		return checkpoint.Entry{}, err
	}
	if reply.Entry == nil || !reply.Entry.Time.Equal(at.UTC().Truncate(time.Millisecond)) {
		return checkpoint.Entry{}, fmt.Errorf("the original actor returned another created checkpoint")
	}
	actual, err := checkpoint.EncodeMetadataV1(reply.Entry.Metadata)
	if err != nil {
		return checkpoint.Entry{}, err
	}
	if !bytes.Equal(expected, actual) {
		return checkpoint.Entry{}, fmt.Errorf("the actual actor changed the original checkpoint metadata")
	}
	return *reply.Entry, nil
}

func (backend *subordinateCheckpointBackendV3) Trim(ctx context.Context, keep int) error {
	_, err := backend.callV3(ctx, checkpointActorRequestV3{Action: "trim", Keep: keep})
	return err
}

func (backend *subordinateCheckpointBackendV3) DiscardCreatedV1(ctx context.Context, entry checkpoint.Entry) error {
	_, err := backend.callV3(ctx, checkpointActorRequestV3{Action: "discard", Entry: &entry})
	return err
}

func (*subordinateCheckpointBackendV3) Select(context.Context, string) (checkpoint.SnapshotV1, error) {
	return nil, &proto.Error{Code: "checkpoint_snapshot_custody", Message: "the running actor cannot issue an independent stopped snapshot loan", Hint: "Keep this session's original checkpoint files until its native snapshot custody producer is available."}
}

func (*subordinateCheckpointBackendV3) PrepareRestore(context.Context, checkpoint.SnapshotV1, checkpoint.TransactionV1, func(context.Context) error) (checkpoint.PreparedRestoreV1, error) {
	return nil, &proto.Error{Code: "checkpoint_stopped_actor", Message: "the original running checkpoint actor cannot replace its live VM disk", Hint: "Use the same original stopped-session storage actor after the whole VM unit has physically joined."}
}

var _ checkpoint.BackendV1 = (*subordinateCheckpointBackendV3)(nil)
