package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type reverseForward struct {
	args    proto.ReversePortV1
	ctx     context.Context
	cancel  context.CancelFunc
	client  *client
	carrier *link.Session
	ready   chan struct{}
	done    chan struct{}
	limit   chan struct{}
	wg      sync.WaitGroup
}

func (s *Supervisor) reverseFrame(c *client, reader *bufio.Reader, frame proto.Frame, first bool) bool {
	if frame.Type != proto.TRequest {
		return false
	}
	var request proto.Request
	if json.Unmarshal(frame.Payload, &request) != nil || request.Op != "port.reverse" {
		return false
	}
	var args api.PortArgsV1
	if !first || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.ID != s.id || args.Port < 1 || args.Port > 65535 {
		c.reply(request.ID, nil, portFailure("reverse forwarding needs one dedicated connection and exact port"))
		return true
	}
	s.owner().serveReverse(c, reader, request.ID, args.Port)
	return true
}

func (s *Supervisor) serveReverse(c *client, reader *bufio.Reader, requestID uint64, port int) {
	s.portMu.Lock()
	s.mu.Lock()
	running := s.rec.phase == host.LifeRunning
	s.mu.Unlock()
	if s.portsClosed || !running || len(s.reverseForwards) >= maxForwardedPorts {
		s.portMu.Unlock()
		c.reply(requestID, nil, portFailure("the sandbox cannot admit another reverse forward"))
		return
	}
	for _, previous := range s.reverseForwards {
		if previous.args.Port == port {
			s.portMu.Unlock()
			c.reply(requestID, nil, portFailure("the guest port already has a reverse forward"))
			return
		}
	}
	lease := host.NewID()
	if s.reverseForwards[lease] != nil {
		s.portMu.Unlock()
		c.reply(requestID, nil, portFailure("the reverse operation identity is already owned"))
		return
	}
	ctx, cancel := context.WithCancel(s.lifetimeContext())
	op := &reverseForward{args: proto.ReversePortV1{PortHeaderV1: proto.PortHeaderV1{Version: 1, Port: port}, Lease: lease},
		ctx: ctx, cancel: cancel, client: c, ready: make(chan struct{}), done: make(chan struct{}), limit: make(chan struct{}, 32)}
	if s.reverseForwards == nil {
		s.reverseForwards = make(map[string]*reverseForward)
	}
	s.reverseForwards[op.args.Lease] = op
	s.portMu.Unlock()
	stopClient := context.AfterFunc(ctx, c.close)
	defer stopClient()
	defer s.finishReverse(op)
	start, stop := context.WithTimeout(ctx, 5*time.Second)
	err := s.ctl.requestDetached(start, proto.CtlReverseStartV1, op.args, nil)
	stop()
	if err != nil {
		c.reply(requestID, nil, portFailure("the guest could not start the requested reverse listener"))
		return
	}
	c.reply(requestID, struct{}{}, nil)
	c.wmu.Lock()
	writeErr := c.writeErr
	c.wmu.Unlock()
	if writeErr != nil || ctx.Err() != nil {
		return
	}
	carrier := link.ContinueReverse(c.conn, reader, link.Supervisor)
	s.portMu.Lock()
	if s.reverseForwards[op.args.Lease] != op || ctx.Err() != nil {
		s.portMu.Unlock()
		_ = carrier.Close()
		return
	}
	op.carrier = carrier
	close(op.ready)
	s.portMu.Unlock()
	s.audit.write("reverse_grant", map[string]any{"port": port})
	select {
	case <-carrier.Done():
	case <-ctx.Done():
	}
}

func (s *Supervisor) finishReverse(op *reverseForward) {
	s.portMu.Lock()
	if s.reverseForwards[op.args.Lease] == op {
		delete(s.reverseForwards, op.args.Lease)
	}
	op.cancel()
	carrier := op.carrier
	s.portMu.Unlock()
	if carrier != nil {
		_ = carrier.Close()
	}
	_ = op.client.conn.Close()
	stop, cancel := context.WithTimeout(s.lifetimeContext(), 5*time.Second)
	_ = s.ctl.requestDetached(stop, proto.CtlReverseStopV1, op.args, nil)
	cancel()
	op.wg.Wait()
	s.audit.write("reverse_revoke", map[string]any{"port": op.args.Port})
	close(op.done)
}

// forwardReverseStream takes guest claims only against the exact retained
// operation. It never chooses a laptop destination or opens a host listener.
func (s *Supervisor) forwardReverseStream(stream *link.Stream) {
	s = s.owner()
	var header proto.ReversePortV1
	if stream.Header(&header) != nil || !api.ExactJSONFields(stream.HeaderBytes(), struct {
		Kind link.Kind `json:"kind"`
		proto.ReversePortV1
	}{}) || header.Version != 1 || !api.ValidSessionID(header.Lease) {
		_ = stream.Close()
		return
	}
	s.portMu.Lock()
	op := s.reverseForwards[header.Lease]
	valid := op != nil && op.args == header && op.ctx.Err() == nil && !s.portsClosed
	if valid {
		select {
		case op.limit <- struct{}{}:
			op.wg.Add(1)
		default:
			valid = false
		}
	}
	s.portMu.Unlock()
	if !valid {
		_ = stream.Close()
		return
	}
	go func() {
		defer op.wg.Done()
		defer func() { <-op.limit }()
		defer stream.Close()
		select {
		case <-op.ready:
		case <-op.ctx.Done():
			return
		}
		s.portMu.Lock()
		carrier := op.carrier
		valid := s.reverseForwards[header.Lease] == op && op.ctx.Err() == nil && !s.portsClosed
		s.portMu.Unlock()
		if !valid || carrier == nil {
			return
		}
		client, err := carrier.Open(op.ctx, link.KindRPort, header.PortHeaderV1)
		if err != nil {
			return
		}
		defer client.Close()
		s.audit.write("reverse_port", map[string]any{"port": header.Port})
		_ = link.RelayDuplex(op.ctx, stream, client)
	}()
}

func (s *Supervisor) closeReverseForwards() {
	s.portMu.Lock()
	ops := make([]*reverseForward, 0, len(s.reverseForwards))
	for _, op := range s.reverseForwards {
		ops = append(ops, op)
		op.cancel()
		op.client.close()
	}
	s.portMu.Unlock()
	for _, op := range ops {
		<-op.done
	}
}
