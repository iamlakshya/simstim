package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// serveEvents transfers only this already verified ordinary observer. The same
// dispatcher is available before the factory starts, so a real boot failure is
// observable without treating a directory or socket as a lifecycle event.
func (s *Supervisor) serveEvents(c *client, reader *bufio.Reader, request proto.Request) error {
	refuse := func(code, message, hint string) error {
		failure := &proto.Error{Code: code, Message: message, Hint: hint}
		body, _ := json.Marshal(proto.Response{ID: request.ID, Error: failure})
		return errors.Join(failure, c.write(proto.TResponse, body))
	}
	if !c.full {
		return refuse(host.ErrInside.Code, host.ErrInside.Message, host.ErrInside.Hint)
	}
	var args api.EventsWatchArgs
	if !api.ExactJSONFields(request.Args, api.EventsWatchArgs{}) || json.Unmarshal(request.Args, &args) != nil || args.V != 1 || args.ID != "" && args.ID != s.id {
		return refuse("bad_request", "the event observer has an invalid version or session", "Use events with the original exact session address.")
	}
	if s.al == nil {
		return refuse("events_unavailable", "the event dispatcher is not available", "Run events again after checking the session's supervisor log.")
	}
	watch := s.al.WatchEvents(args.Follow)
	defer watch.Close()
	if err := watch.Err(); err != nil {
		var native *proto.Error
		if errors.As(err, &native) {
			return refuse(native.Code, native.Message, native.Hint)
		}
		var local *alerts.Error
		if errors.As(err, &local) {
			return refuse("events_unavailable", local.Msg, local.Hint)
		}
		return refuse("events_unavailable", err.Error(), "Run events again after checking the supervisor log.")
	}
	body, _ := json.Marshal(proto.Response{ID: request.ID, OK: true})
	if err := c.write(proto.TResponse, body); err != nil {
		return err
	}
	if err := c.conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	carrier := link.ContinueEvents(c.conn, reader, link.Supervisor)
	defer carrier.Close()
	ctx, cancel := context.WithCancel(s.lifetimeContext())
	defer cancel()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); _ = carrier.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	root := s.owner()
	stream, err := carrier.Open(ctx, link.KindEvents, api.EventsWatchArgs{V: 1, ID: root.id, Follow: args.Follow})
	if err != nil {
		return err
	}
	control, joined := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(joined)
		var one [1]byte
		n, err := stream.Read(one[:])
		if n != 0 {
			err = &proto.Error{Code: "protocol", Message: "an event observer sent an input payload", Hint: "Use a client and helper with matching event stream support."}
		}
		control <- err
	}()
	defer func() { _ = carrier.Close(); <-joined }()
	write := func(event alerts.Event) error {
		if args.ID != "" && event.Session.ID != args.ID {
			return nil
		}
		if event.V != alerts.Version || !alerts.IsStreamKind(event.Event) {
			return &proto.Error{Code: "protocol", Message: "the dispatcher produced an unsupported event", Hint: "Update the complete host helper."}
		}
		line := append(event.JSON(), '\n')
		if len(line) > proto.MaxFrame {
			return &proto.Error{Code: "protocol", Message: "the event exceeds the protocol frame bound", Hint: "Check the supervisor log and update the helper."}
		}
		wait := c.writeWait
		if wait <= 0 {
			wait = clientWriteWait
		}
		if err := stream.SetWriteDeadline(time.Now().Add(wait)); err != nil {
			return err
		}
		n, err := stream.Write(line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		return err
	}
	for _, event := range watch.Snapshot {
		if err := write(event); err != nil {
			return err
		}
	}
	for {
		select {
		case event, open := <-watch.Events:
			if !open {
				if err := watch.Err(); err != nil {
					return err
				}
				// Close uses the link's existing five-second linger cap, rather
				// than extending it to the per-record output deadline.
				if err := stream.SetWriteDeadline(time.Time{}); err != nil {
					return err
				}
				return stream.Close()
			}
			if err := write(event); err != nil {
				return err
			}
		case err := <-control:
			if err == nil || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-carrier.Done():
			return carrier.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
