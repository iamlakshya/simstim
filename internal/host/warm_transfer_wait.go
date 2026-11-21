//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// One actual Warm reader retains its distinct rights class while cancellation
// and the original transfer reply join. A cancel ACK may race the transfer;
// neither ACK supplies Source or lets the next ordinary reader lose rights.
type warmTransferWaitV1 struct {
	warm       *WarmResumeV1
	request    uint64
	written    chan error
	stop       func() bool
	seenCancel bool
	finished   bool
	failure    error
}

func beginWarmTransferWaitV1(ctx context.Context, warm *WarmResumeV1, request uint64) *warmTransferWaitV1 {
	wait := &warmTransferWaitV1{warm: warm, request: request, written: make(chan error, 1)}
	wait.stop = context.AfterFunc(ctx, func() { wait.written <- warm.writeWarmControlV1(5, proto.NativeWarmCancelV1) })
	return wait
}

func (wait *warmTransferWaitV1) readV1() (proto.Frame, proto.Response, error) {
	owner := wait.warm.owner
	for {
		frame, err := owner.line.ReadFrame()
		if err != nil {
			return frame, proto.Response{}, err
		}
		var response proto.Response
		if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil {
			return frame, response, errors.Join(auxiliaryUnavailableV1("the original native transfer changed its response frame"), owner.line.RejectAncillary())
		}
		// This checks only the already-held native sender for receiving/cleanup.
		// A cancelled worker context cannot make new Source: its factory still
		// checks that context and all original issuer objects before consume.
		if err := owner.checkCurrentStopSenderV1(context.Background()); err != nil {
			return frame, response, errors.Join(err, owner.line.RejectAncillary())
		}
		if response.ID == wait.request {
			return frame, response, nil
		}
		if response.ID != 5 || wait.seenCancel {
			return frame, response, errors.Join(auxiliaryUnavailableV1("the native transfer received another original request's reply"), owner.line.RejectAncillary())
		}
		if err := owner.line.RejectAncillary(); err != nil {
			return frame, response, err
		}
		var state proto.NativeStopStateV1
		if !response.OK || !api.ExactJSONFields(response.Result, state) || json.Unmarshal(response.Result, &state) != nil || state != owner.state {
			return frame, response, auxiliaryUnavailableV1("the native transfer cancellation did not join its original scope")
		}
		wait.seenCancel = true
	}
}

func (wait *warmTransferWaitV1) closeV1() error {
	if wait == nil {
		return nil
	}
	if wait.finished {
		return wait.failure
	}
	if !wait.stop() {
		wait.failure = <-wait.written
		if wait.failure == nil && !wait.seenCancel {
			wait.failure = wait.warm.readNativeStateV1(5)
		}
	}
	wait.finished = true
	return wait.failure
}
