package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// serveExec takes this dedicated client's reader/writer only after an ordinary
// capability-bearing hello. Commands go exclusively over the guest link.
func (s *Supervisor) serveExec(c *client, r *bufio.Reader, req proto.Request) {
	var a api.ExecArgs
	if json.Unmarshal(req.Args, &a) != nil {
		c.reply(req.ID, nil, execRefusal("invalid command request"))
		return
	}
	if err := a.Normalise(); err != nil {
		c.reply(req.ID, nil, execRefusal(err.Error()))
		return
	}
	s.mu.Lock()
	if a.ID != s.id || s.rec.phase != host.LifeRunning {
		s.mu.Unlock()
		c.reply(req.ID, nil, execRefusal("the sandbox is not running"))
		return
	}
	cwd := s.m.Cwd
	s.mu.Unlock()
	id := host.NewID()
	ctx, cancel := context.WithTimeout(c.requestContextV1(s.owner().lifetimeContext()), bootTimeout)
	defer cancel()
	err := s.ctl.request(ctx, proto.CtlExec, proto.CtlExecArgs{ID: id, Argv: a.Argv, Cwd: cwd, TimeoutMS: a.TimeoutMS}, nil)
	if err != nil {
		c.reply(req.ID, nil, execRefusal(err.Error()))
		return
	}
	guest, err := s.link.Open(ctx, link.KindExec, proto.ExecHeader{ID: id})
	if err != nil {
		c.reply(req.ID, nil, execRefusal(err.Error()))
		return
	}
	defer guest.Close()
	c.reply(req.ID, struct{}{}, nil)
	// The ordinary client writer has no further producers on this connection.
	carrier := link.ContinueExec(c.conn, r, link.Supervisor)
	defer carrier.Close()
	output, err := carrier.Open(ctx, link.KindExec, proto.ExecHeader{ID: id})
	if err != nil {
		return
	}
	defer output.Close()
	inputDone := make(chan struct{})
	go func() { defer close(inputDone); _, _ = io.Copy(guest, output); _ = guest.CloseWrite() }()
	// Forward opaque records; the trusted client decodes them. No terminal
	// escape, key, NUL or newline is interpreted on this path.
	_, _ = io.Copy(output, guest)
	_ = output.CloseWrite()
	_ = output.SetWriteDeadline(time.Now().Add(clientWriteWait))
	_ = output.Close()
	_ = carrier.Close()
	_ = guest.SetReadDeadline(time.Now())
	_ = guest.Close()
	<-inputDone
}
func execRefusal(message string) *proto.Error {
	return &proto.Error{Code: "exec_failed", Message: message, Hint: "Check the command and resume the sandbox, then run it again."}
}

func (s *Supervisor) execFrame(c *client, r *bufio.Reader, f proto.Frame, first bool) bool {
	if f.Type != proto.TRequest {
		return false
	}
	var req proto.Request
	if json.Unmarshal(f.Payload, &req) != nil || req.Op != "exec" {
		return false
	}
	if !first {
		c.reply(req.ID, nil, execRefusal("exec requires a dedicated connection"))
		return true
	}
	s.serveExec(c, r, req)
	return true
}
