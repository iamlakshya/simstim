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
	"golang.org/x/sys/unix"
)

// RunPurgeRelayProcessV3 owns one raw DATA request, then the caller exits. It
// performs no normal placement admission, installation or Source construction.
func RunPurgeRelayProcessV3() int {
	image, err := newRawPurgeRelayImageV3()
	if err != nil {
		return 1
	}
	defer image.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	inputDone := make(chan struct{})
	stopInput := context.AfterFunc(ctx, func() { defer close(inputDone); _ = os.Stdin.Close() })
	defer func() {
		if !stopInput() {
			<-inputDone
		}
	}()
	request, data, err := readRawPurgeRelayRequestV3(os.Stdin)
	if err != nil {
		_ = writeRawPurgeRelayResponseV3(1, api.PurgeResult{}, err)
		return 1
	}
	requestContext, join, err := watchRawPurgeRelayInputV3(ctx)
	if err != nil {
		_ = writeRawPurgeRelayResponseV3(request.ID, api.PurgeResult{}, err)
		return 1
	}
	defer join()
	result, failure := relayExistingPurgeRequestV3(requestContext, image, request, data)
	if err := writeRawPurgeRelayResponseV3(request.ID, result, failure); err != nil {
		return 1
	}
	if failure != nil {
		return 1
	}
	return 0
}

func readRawPurgeRelayRequestV3(input *os.File) (proto.Request, purgeRequestDataV3, error) {
	frame, err := proto.ReadFrom(input)
	if err != nil {
		return proto.Request{}, purgeRequestDataV3{}, err
	}
	request, data, err := decodePurgeRequestDataV3(frame)
	if err != nil {
		return request, data, err
	}
	if request.Op == purgeBeginV3 {
		return request, data, placementError("a fresh raw relay cannot create a replacement removal vector")
	}
	return request, data, nil
}

func relayExistingPurgeRequestV3(ctx context.Context, image *purgeRelayImageV3, request proto.Request, data purgeRequestDataV3) (api.PurgeResult, error) {
	return relayOriginalPurgeRequestV3(ctx, image, request, data, nil)
}

func relayOriginalPurgeRequestV3(ctx context.Context, image *purgeRelayImageV3, request proto.Request, data purgeRequestDataV3, original *purgeAttemptExecV1) (api.PurgeResult, error) {
	var result api.PurgeResult
	if ctx == nil || image == nil || request.Op == purgeBeginV3 {
		return result, placementError("the raw relay has no fixed existing-owner continuation")
	}
	target, err := openPurgeRelayTargetForOriginalV3(ctx, image, data.Args, original)
	if err != nil {
		return result, err
	}
	defer target.Close()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = target.line.conn.Close() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	if err := target.line.WriteFrame(proto.TRequest, request); err != nil {
		return result, err
	}
	frame, err := target.line.ReadFrame()
	if err != nil {
		return result, err
	}
	if err := target.line.RejectAncillary(); err != nil {
		return result, err
	}
	sender, err := target.line.currentSender()
	if err != nil {
		return result, err
	}
	if err := target.checkSenderV3(ctx, sender); err != nil {
		return result, err
	}
	result, err = decodePurgeRelayResponseDataV3(frame, request.ID, data.Args)
	if err != nil {
		return result, err
	}

	if request.Op == purgeCloseV3 {
		// This acknowledges public DATA already checked while its actual native
		// sender is live. Physical cleanup preceded this response and ACK.
		if err := target.line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, OK: true}); err != nil {
			return result, err
		}
	}
	return result, nil
}

func writeRawPurgeRelayResponseV3(id uint64, result api.PurgeResult, failure error) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	response := proto.Response{ID: id, OK: failure == nil, Result: raw}
	if failure != nil {
		var wire *proto.Error
		if errors.As(failure, &wire) {
			response.Error = wire
		} else {
			response.Error = UserError("purge_relay", "the original removal could not settle this continuation: "+failure.Error(), "Keep the host saved and continue the same live removal. Do not reconstruct native custody from a saved report.")
		}
	}
	return proto.NewWriter(os.Stdout).WriteJSON(proto.TResponse, response)
}

// This observer does not read any input bytes. EOF on the original raw pipe
// cancels the relay's socket, which cancels only the owner's current request.
func watchRawPurgeRelayInputV3(parent context.Context) (context.Context, func(), error) {
	fd, err := unix.FcntlInt(os.Stdin.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer unix.Close(fd)
		defer cancel()
		for ctx.Err() == nil {
			polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLHUP | unix.POLLRDHUP}}
			count, err := unix.Poll(polls, 100)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || count != 0 {
				return
			}
		}
	}()
	return ctx, func() { cancel(); <-done }, nil
}

func decodePurgeRelayResponseDataV3(frame proto.Frame, id uint64, args api.PurgeArgs) (result api.PurgeResult, returned error) {
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !api.ExactJSONFields(response.Result, result) || json.Unmarshal(response.Result, &result) != nil || result.HostID != args.HostID || result.Nonce != args.Nonce {
		return result, placementError("the actual raw relay response changed the original target or request")
	}
	if !response.OK && response.Error != nil {
		return result, response.Error
	}
	if !response.OK || response.Error != nil {
		return result, placementError("the raw relay target returned inconsistent fixed result data")
	}
	return result, nil
}
