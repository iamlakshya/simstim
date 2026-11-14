package host

import (
	"bufio"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// exec transfers this bridge connection once. It never executes a command on
// the host, including for an unwalled or older session.
func (b *bridge) exec(r *bufio.Reader, req proto.Request) error {
	var a api.ExecArgs
	if err := json.Unmarshal(req.Args, &a); err != nil {
		b.reply(req.ID, nil, UserError("bad_request", "invalid exec request", "Pass a command and its session address."))
		return err
	}
	if err := a.Normalise(); err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	m, err := LoadManifest(a.ID)
	if err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	if m.Wall == nil {
		err = UserError("not_walled", "this session has no sandbox for exec", "Use shell for the deprecated unwalled terminal, or start a walled session.")
		b.reply(req.ID, nil, err)
		return err
	}
	if m.Life != nil && m.Life.Phase == LifeStopped {
		err = UserError("sandbox_stopped", "the sandbox is stopped", "Resume its session, then run exec again.")
		b.reply(req.ID, nil, err)
		return err
	}
	line, hello, err := dialSessionHolder(a.ID, 10*time.Second)
	if err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	defer line.c.Close()
	if err = requireSupervisorFeatures(hello, api.FeatureExecStream); err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	if err = line.w.WriteJSON(proto.TRequest, req); err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	frame, err := proto.Read(line.r)
	if err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	var resp proto.Response
	if frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &resp) != nil || resp.ID != req.ID {
		err = UserError("protocol", "the supervisor sent an invalid exec answer", "Stop and resume the session.")
		b.reply(req.ID, nil, err)
		return err
	}
	if err = b.w.Write(proto.TResponse, frame.Payload); err != nil {
		return err
	}
	if !resp.OK {
		return nil
	}
	_ = line.c.SetDeadline(time.Time{})
	done := make(chan error, 1)
	go func() { done <- proto.RelayStreams(line.r, b.w); line.c.Close() }()
	// The main bridge reader remains the only reader of its borrowed input.
	// A final client close ends this loop; no flags/deadlines on stdin change.
	err = proto.RelayStreams(r, line.w)
	line.c.Close()
	<-done
	return err
}
