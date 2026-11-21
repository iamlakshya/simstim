//go:build linux

package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func serveAuxiliaryReadOnlyV1(ctx context.Context, outer *link.Session, inner *auxiliaryTransportV1) (returned error) {
	lifetime, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); returned = errors.Join(returned, outer.Close(), inner.Close()); workers.Wait() }()
	// Auxiliary guests receive no egress/DNS/SSH agent/port capability. Closing
	// these actual guest-opened streams cannot turn them into outside sockets.
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			stream, err := inner.session.Accept(lifetime)
			if err != nil {
				return
			}
			_ = stream.Close()
		}
	}()
	controlUsed := false
	for {
		stream, err := outer.Accept(lifetime)
		if err != nil {
			return err
		}
		if stream.Kind() == link.KindCtl {
			if controlUsed {
				_ = stream.Close()
				return auxiliaryUnavailableV1("the auxiliary reader already owns its sole control stream")
			}
			controlUsed = true
			workers.Add(1)
			go func() { defer workers.Done(); defer cancel(); _ = serveAuxiliaryControlV1(lifetime, stream, inner) }()
			continue
		}
		header, err := auxiliaryReadHeaderV1(stream)
		if err != nil {
			_ = stream.Close()
			return err
		}
		target, err := inner.Open(lifetime, stream.Kind(), header)
		if err != nil {
			_ = stream.Close()
			return err
		}
		workers.Add(1)
		go func() { defer workers.Done(); relayAuxiliaryReadV1(lifetime, stream, target) }()
	}
}
func auxiliaryReadHeaderV1(stream *link.Stream) (any, error) {
	fields, fieldErr := api.JSONObjectFields(stream.HeaderBytes(), "kind", "list", "v", "path", "offset", "max_bytes", "purpose", "id")
	if fieldErr != nil {
		return nil, auxiliaryUnavailableV1("the auxiliary read stream has malformed fields")
	}
	delete(fields, "kind")
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	switch stream.Kind() {
	case link.KindGet:
		var header proto.FileGetHeaderV1
		if !api.ExactJSONFields(raw, header) || json.Unmarshal(raw, &header) != nil || header.Version != proto.FileTransferVersionV1 {
			return nil, auxiliaryUnavailableV1("the auxiliary copy stream has an unsupported declaration")
		}
		return header, nil
	case link.KindBundle:
		var header proto.ExecHeader
		if !api.ExactJSONFields(raw, header) || json.Unmarshal(raw, &header) != nil || header.Purpose != "pull" || !api.ValidSessionID(header.ID) {
			return nil, auxiliaryUnavailableV1("the auxiliary bundle stream is not a fixed pull exporter")
		}
		return header, nil
	default:
		return nil, auxiliaryUnavailableV1("the auxiliary lifetime refuses this stream kind")
	}
}
func serveAuxiliaryControlV1(ctx context.Context, stream *link.Stream, inner *auxiliaryTransportV1) error {
	defer stream.Close()
	reader := bufio.NewReaderSize(stream, auxiliaryControlMaxV1)
	writer := proto.NewWriter(stream)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = stream.Close() })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	for {
		frame, err := proto.ReadLimit(reader, auxiliaryControlMaxV1)
		if err != nil {
			return err
		}
		var request proto.Request
		if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID == 0 {
			return auxiliaryUnavailableV1("the auxiliary control frame has no exact request")
		}
		if request.Op != proto.CtlBundlePrepare && request.Op != proto.CtlBundleCancel && request.Op != proto.CtlPurgeWorkInspectV1 {
			return auxiliaryUnavailableV1("the auxiliary reader refuses that control operation")
		}
		var result json.RawMessage
		err = inner.Call(ctx, request.Op, request.Args, &result)
		response := proto.Response{ID: request.ID, OK: err == nil, Result: result}
		if err != nil {
			var wire *proto.Error
			if errors.As(err, &wire) {
				response.Error = wire
			} else {
				response.Error = &proto.Error{Code: "auxiliary_unavailable", Message: "the actual auxiliary exporter failed", Hint: "Keep the sandbox and retry after its native cleanup finishes."}
			}
		}
		raw, encodeErr := json.Marshal(response)
		if encodeErr != nil {
			return encodeErr
		}
		if len(raw) > auxiliaryControlMaxV1-1 {
			return auxiliaryUnavailableV1("the auxiliary response exceeds the existing guest frame bound")
		}
		_ = stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := writer.WriteCtx(ctx, proto.TResponse, raw); err != nil {
			return err
		}
	}
}
func relayAuxiliaryReadV1(ctx context.Context, left, right *link.Stream) {
	stop := context.AfterFunc(ctx, func() { _ = left.Close(); _ = right.Close() })
	defer stop()
	defer left.Close()
	defer right.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := io.Copy(right, left)
		if err != nil {
			_ = left.Close()
			_ = right.Close()
		} else {
			_ = right.CloseWrite()
		}
	}()
	_, err := io.Copy(left, right)
	if err != nil {
		_ = left.Close()
		_ = right.Close()
	} else {
		_ = left.CloseWrite()
	}
	<-done
}
