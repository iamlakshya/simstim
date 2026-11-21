//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The actual original fixed execution is selected BEFORE the first hello is
// observed. Neither a code tuple nor a saved pathname selects this issuer.
func (owner *purgeAttemptExecV1) admitOriginalCodeV3(ctx context.Context) error {
	if owner == nil || ctx == nil {
		return placementError("the purge code admission has no original execution")
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if err := owner.connectOriginalV1(startup); err == nil {
			break
		} else if err := errors.Join(startup.Err(), owner.checkCreatorV1(startup)); err != nil {
			return err
		}
		select {
		case <-startup.Done():
			return startup.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if deadline, ok := startup.Deadline(); ok {
		_ = owner.line.SetDeadline(deadline)
	}
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return err
	}
	if err := owner.line.acceptOriginalPurgeAttemptHelloV2(startup, frame, owner); err != nil {
		return err
	}
	if err := owner.line.WriteNativeHelloV1(proto.Hello{Role: "purge-data-parent-v3"}); err != nil {
		return err
	}
	request, err := purgeRequestFrameV3(1, purgeBeginV3, owner.args)
	if err != nil {
		return err
	}
	if err := owner.line.WriteFrame(proto.TRequest, request); err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := owner.checkCreatorLockedV1(startup); err != nil {
		return err
	}
	// This is the only private code-custody writer. The SAME original native
	// creation and immutable Cmd have passed the creator receiver above.
	// Mark retention BEFORE the first physical loan can become observable;
	// an incomplete write or lost ACK cannot release a running child.
	owner.admissionSent = true
	for _, file := range []*os.File{owner.directory, owner.code.lease} {
		if err := owner.checkCreatorLockedV1(startup); err != nil {
			return err
		}
		if err := owner.line.writeLoan(request, file); err != nil {
			return err
		}
	}
	return owner.line.SetDeadline(time.Time{})
}

func purgeRequestFrameV3(id uint64, operation string, args api.PurgeArgs) (proto.Request, error) {
	raw, err := json.Marshal(purgeRequestDataV3{Version: 3, Args: args})
	if err != nil {
		return proto.Request{}, err
	}
	request := proto.Request{ID: id, Op: operation, Args: raw}
	if id == 0 || !ValidHostID(args.HostID) || !ValidHostID(args.Nonce) || operation != purgeBeginV3 && operation != purgeContinueV3 && operation != purgeAbortV3 && operation != purgeCloseV3 {
		return proto.Request{}, placementError("the actual purge request has another closed operation or invalid original data")
	}
	return request, nil
}

func readOriginalPurgeResponseV3(ctx context.Context, owner *purgeAttemptExecV1, id uint64) (api.PurgeResult, error) {
	var result api.PurgeResult
	if owner == nil || owner.line == nil || owner.peer == nil {
		return result, placementError("the purge response has no original admitted execution")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = owner.line.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return result, err
	}
	if err := owner.line.RejectAncillary(); err != nil {
		return result, err
	}
	sender, err := owner.line.currentSender()
	if err != nil || sender.pid != owner.process.Pid || sender.uid != owner.peer.uid {
		return result, errors.Join(placementError("the purge response differs from its original actual creator"), err)
	}
	if err := owner.peer.checkNativeSender(sender); err != nil {
		return result, err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !api.ExactJSONFields(response.Result, result) || json.Unmarshal(response.Result, &result) != nil {
		return result, placementError("the original purge owner supplied an invalid fixed response")
	}
	if result.HostID != "" && (result.HostID != owner.args.HostID || result.Nonce != owner.args.Nonce) {
		return result, placementError("the original purge report names another attempt")
	}
	if response.OK && response.Error == nil {
		return result, nil
	}
	if !response.OK && response.Error != nil {
		return result, response.Error
	}
	return result, placementError("the original purge response has inconsistent success data")
}
