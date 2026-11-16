package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type bridgeEventResult struct {
	source *bridgeEventSource
	event  *alerts.Event
	err    error
}

type bridgeEventSource struct {
	root      string
	publisher string
	ctx       context.Context
	cancel    context.CancelFunc
	line      *StartupLineV1
	mux       *link.Session
	stream    *link.Stream
	done      chan struct{}
	once      sync.Once
}

func openBridgeEventSource(ctx context.Context, id, target string, follow bool, output chan<- bridgeEventResult) (source *bridgeEventSource, failure error) {
	lifetime, cancel := context.WithCancel(ctx)
	startup, endStartup := context.WithTimeout(lifetime, 10*time.Second)
	defer endStartup()
	connection, err := (&net.Dialer{}).DialContext(startup, "unix", paths.Socket(id))
	if err != nil {
		cancel()
		return nil, err
	}
	line, err := NewStartupLineV1(connection)
	if err != nil {
		cancel()
		_ = connection.Close()
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			cancel()
			_ = line.Close()
		}
	}()
	joined := make(chan struct{})
	stop := context.AfterFunc(startup, func() { defer close(joined); _ = line.Close() })
	endCancellation := sync.OnceFunc(func() {
		if !stop() {
			<-joined
		}
	})
	defer endCancellation()
	deadline, _ := startup.Deadline()
	if err := line.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if err := line.WriteFrame(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		return nil, err
	}
	frame, err := line.ReadFrame()
	if err != nil {
		return nil, err
	}
	if err := line.RejectAncillary(); err != nil {
		return nil, err
	}
	var hello proto.Hello
	if frame.Type != proto.THello || json.Unmarshal(frame.Payload, &hello) != nil || hello.Role != "supervisor" {
		return nil, UserError("protocol", "the event source did not send a supervisor hello", "Stop and resume the session with the current helper.")
	}
	if err := requireSupervisorFeatures(hello, api.FeatureEvents); err != nil {
		return nil, err
	}
	args, _ := json.Marshal(api.EventsWatchArgs{V: 1, ID: target, Follow: follow})
	if err := line.WriteFrame(proto.TRequest, proto.Request{ID: 1, Op: "events.watch", Args: args}); err != nil {
		return nil, err
	}
	frame, err = line.ReadFrame()
	if err != nil {
		return nil, err
	}
	if err := line.RejectAncillary(); err != nil {
		return nil, err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != 1 {
		return nil, UserError("protocol", "the supervisor returned an invalid event response", "Update the client and complete host helper.")
	}
	if !response.OK {
		if response.Error != nil {
			return nil, response.Error
		}
		return nil, UserError("protocol", "the supervisor refused events without an error", "Check the session's supervisor log.")
	}
	if err := line.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(line.OrdinaryReader(), 64<<10)
	mux := link.ContinueEvents(connection, reader, link.Guest)
	stream, err := mux.Accept(startup)
	if err != nil {
		_ = mux.Close()
		return nil, err
	}
	var header api.EventsWatchArgs
	if stream.Kind() != link.KindEvents || stream.Header(&header) != nil || header.V != 1 || !api.ValidSessionID(header.ID) || header.Follow != follow {
		_ = mux.Close()
		return nil, UserError("protocol", "the native event source header is invalid", "Use a client and complete helper with matching event support.")
	}
	endCancellation()
	if err := startup.Err(); err != nil {
		_ = mux.Close()
		return nil, err
	}
	source = &bridgeEventSource{root: header.ID, ctx: lifetime, cancel: cancel, line: line, mux: mux, stream: stream, done: make(chan struct{})}
	keep = true
	go source.read(target, output)
	return source, nil
}

func (s *bridgeEventSource) read(target string, output chan<- bridgeEventResult) {
	defer close(s.done)
	reader := bufio.NewReader(s.stream)
	send := func(result bridgeEventResult) bool {
		select {
		case output <- result:
			return true
		case <-s.ctx.Done():
			return false
		}
	}
	for {
		if _, err := reader.Peek(1); err != nil {
			if errors.Is(err, io.EOF) && s.stream.PeerWriteClosed() {
				err = nil
			}
			_ = send(bridgeEventResult{source: s, err: err})
			return
		}
		line, err := proto.ReadJSONLine(reader)
		if err != nil {
			_ = send(bridgeEventResult{source: s, err: err})
			return
		}
		event, err := alerts.DecodeStreamEvent(line, target)
		if err == nil {
			_, err = alerts.StreamEventIdentity(event)
		}
		if err == nil && s.publisher != "" && s.publisher != event.Stream {
			err = UserError("protocol", "one native event source changed its publisher identity", "Read events again with the current complete helper.")
		}
		if err != nil {
			_ = send(bridgeEventResult{source: s, err: err})
			return
		}
		s.publisher = event.Stream
		if !send(bridgeEventResult{source: s, event: &event}) {
			return
		}
	}
}

func (s *bridgeEventSource) close() {
	s.once.Do(func() { s.cancel(); _ = s.mux.Close(); _ = s.line.Close(); <-s.done })
}
