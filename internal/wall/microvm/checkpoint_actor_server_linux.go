//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Created only inside the already admitted actual C-created B. The store
// stays with B's original rooted disk, through actual request and cleanup.
type checkpointActorStoreV3 struct {
	original *readonlyControlDiskV2
	store    *checkpoint.Store
	failure  error
}

func (owner *readonlyControlDiskV2) checkCheckpointActorV3(ctx context.Context) error {
	if owner == nil || owner.root == nil || owner.disk == nil || owner.parent == nil || ctx == nil {
		return fmt.Errorf("the original checkpoint actor lost its actual rooted disk")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := cachefs.ValidateFile(owner.root, SessionDiskName, owner.disk, 0o600)
	if err != nil || info.Size() != owner.bytes {
		return errors.Join(err, fmt.Errorf("the original actor disk changed its anchored identity or size"))
	}
	actual, err := owner.root.Stat(".")
	parent, parentErr := owner.parent.Stat()
	if err != nil || parentErr != nil || !os.SameFile(actual, parent) {
		return errors.Join(err, parentErr, fmt.Errorf("the checkpoint actor changed its actual original VM root"))
	}
	return ctx.Err()
}

func checkpointActorErrorV3(err error) *proto.Error {
	if err == nil {
		return nil
	}
	var wire *proto.Error
	if errors.As(err, &wire) {
		copy := *wire
		return &copy
	}
	return &proto.Error{Code: "checkpoint", Message: "the original checkpoint actor failed: " + err.Error(), Hint: "Keep the original session and check its runtime and free disk space before retrying."}
}

func (server *runnerServer) checkpointOperationV3(ctx context.Context, request *checkpointActorRequestV3) *checkpointActorReplyV3 {
	reply := &checkpointActorReplyV3{Version: 3}
	fail := func(err error) *checkpointActorReplyV3 { reply.Error = checkpointActorErrorV3(err); return reply }
	if request == nil || request.Version != 3 || server.readonlyDisk == nil {
		return fail(fmt.Errorf("this control stream has no actual original checkpoint actor"))
	}
	reply.Action = request.Action
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > checkpointActorFrameV3/2 {
		return fail(errors.Join(err, fmt.Errorf("the fixed actor request exceeds its bound")))
	}
	owner := server.readonlyDisk
	if err := owner.checkCheckpointActorV3(ctx); err != nil {
		return fail(err)
	}
	if owner.checkpoint == nil {
		// Adopt this exact partial resource owner before any constructor error.
		owner.checkpoint = &checkpointActorStoreV3{original: owner}
		owner.checkpoint.store, owner.checkpoint.failure = checkpoint.OpenActorRootV3(ctx, owner.root, owner.disk, owner.checkCheckpointActorV3)
	}
	actor := owner.checkpoint
	if actor.original != owner || actor.failure != nil || actor.store == nil {
		return fail(errors.Join(actor.failure, fmt.Errorf("the original actor store did not complete; retain it for native teardown")))
	}
	backend := checkpoint.NewLocalBackendV1(actor.store)
	switch request.Action {
	case "probe":
		err = backend.Probe(ctx)
	case "space":
		err = backend.CheckSpaceV1(ctx, request.Needed)
	case "list":
		reply.Entry, err = actor.store.ListPageV3(ctx, request.After)
	case "create":
		var entry checkpoint.Entry
		entry, err = backend.Create(ctx, request.When, request.Metadata, request.Keep)
		if err == nil {
			reply.Entry = &entry
		}
	case "trim":
		err = backend.Trim(ctx, request.Keep)
	case "discard":
		if request.Entry == nil {
			err = fmt.Errorf("the actual creator has no original checkpoint to discard")
		} else {
			err = backend.DiscardCreatedV1(ctx, *request.Entry)
		}
	default:
		err = fmt.Errorf("the running checkpoint actor cannot perform this fixed operation")
	}
	if err == nil {
		err = owner.checkCheckpointActorV3(ctx)
	}
	if err != nil {
		return fail(err)
	}
	return reply
}

func (server *runnerServer) closeCheckpointActorV3() error {
	if server.readonlyDisk == nil || server.readonlyDisk.checkpoint == nil {
		return nil
	}
	actor := server.readonlyDisk.checkpoint
	if actor.store == nil {
		return actor.failure
	}
	// The actual serve goroutine has joined before this bounded resource close.
	// B's remaining original native process still belongs to the outside owner.
	return errors.Join(actor.failure, actor.store.Close())
}
