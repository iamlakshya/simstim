//go:build linux

package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const auxiliaryControlMaxV1 = 64 << 10

var auxiliaryOpenersV1 = map[link.Kind]link.Side{link.KindCtl: link.Supervisor, link.KindGet: link.Supervisor, link.KindBundle: link.Supervisor}

type auxiliaryLineConnectionV1 struct {
	net.Conn
	line *StartupLineV1
}

func (connection auxiliaryLineConnectionV1) Read(data []byte) (int, error) {
	return connection.line.OrdinaryReader().Read(data)
}

type auxiliaryTransportV1 struct {
	session *link.Session
	ctl     *link.Stream
	reader  *bufio.Reader
	writer  *proto.Writer
	turn    chan struct{}
	next    uint64
	once    sync.Once
}

func newAuxiliaryTransportV1(ctx context.Context, session *link.Session) (*auxiliaryTransportV1, error) {
	ctl, err := session.Open(ctx, link.KindCtl, struct{}{})
	if err != nil {
		return nil, err
	}
	return &auxiliaryTransportV1{session: session, ctl: ctl, reader: bufio.NewReaderSize(ctl, auxiliaryControlMaxV1), writer: proto.NewWriter(ctl), turn: make(chan struct{}, 1)}, nil
}
func (transport *auxiliaryTransportV1) Call(ctx context.Context, operation string, args, out any) error {
	select {
	case transport.turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-transport.turn }()
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	transport.next++
	request := proto.Request{ID: transport.next, Op: operation, Args: raw}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(encoded) > auxiliaryControlMaxV1-1 {
		return auxiliaryUnavailableV1("the auxiliary control request exceeds the existing guest frame bound")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = transport.ctl.SetDeadline(deadline)
	} else {
		_ = transport.ctl.SetDeadline(time.Time{})
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = transport.ctl.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = transport.ctl.SetDeadline(time.Time{})
	}()
	if err := transport.writer.WriteCtx(ctx, proto.TRequest, encoded); err != nil {
		_ = transport.Close()
		return err
	}
	// Heartbeats during the fixed exporter are advisory. Their bounded frames
	// never extend the caller's original deadline or create execution permission.
	for {
		frame, err := proto.ReadLimit(transport.reader, auxiliaryControlMaxV1)
		if err != nil {
			_ = transport.Close()
			return err
		}
		if frame.Type == proto.TEvent {
			continue
		}
		var response proto.Response
		if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != request.ID {
			_ = transport.Close()
			return auxiliaryUnavailableV1("the auxiliary control response changed its request identity")
		}
		if !response.OK {
			if response.Error != nil {
				return response.Error
			}
			return auxiliaryUnavailableV1("the actual guest refused its closed auxiliary operation")
		}
		if out != nil {
			if err := json.Unmarshal(response.Result, out); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
}
func (transport *auxiliaryTransportV1) Open(ctx context.Context, kind link.Kind, header any) (*link.Stream, error) {
	if kind != link.KindGet && kind != link.KindBundle {
		return nil, auxiliaryUnavailableV1("the auxiliary lifetime permits only read-only copy and bundle streams")
	}
	return transport.session.Open(ctx, kind, header)
}
func (transport *auxiliaryTransportV1) Close() error {
	if transport == nil {
		return nil
	}
	var err error
	transport.once.Do(func() { err = errors.Join(transport.ctl.Close(), transport.session.Close()) })
	return err
}
