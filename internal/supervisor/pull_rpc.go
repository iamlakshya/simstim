package supervisor

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) pullRequest(c *client, req proto.Request) bool {
	if !host.IsPullOp(req.Op) {
		return false
	}
	c.pullMu.Lock()
	if c.pullClosed {
		c.pullMu.Unlock()
		return true
	}
	if c.pull == nil {
		owner, err := host.NewPullRPC(c.requestContextV1(s.owner().lifetimeContext()), s.openPullSource)
		if err != nil {
			c.pullMu.Unlock()
			c.reply(req.ID, nil, pullFailure(err))
			return true
		}
		c.pull = owner
	}
	owner := c.pull
	c.pullMu.Unlock()
	// The canonical full-peer reader calls this synchronously. Admission
	// reserves the nonce before a later frame can schedule its abort.
	run, err := owner.Prepare(req)
	if err != nil {
		c.reply(req.ID, nil, pullFailure(err))
		return true
	}
	go func(ctx context.Context) {
		value, err := run(ctx)
		var wire *proto.Error
		if err != nil {
			wire = pullFailure(err)
		}
		c.reply(req.ID, value, wire)
	}(owner.Context())
	return true
}

func (s *Supervisor) pullProgress(c *client, raw []byte) bool {
	progress, err := api.DecodePullOutputProgress(raw)
	if err != nil {
		return false
	}
	c.pullMu.Lock()
	owner, closed := c.pull, c.pullClosed
	c.pullMu.Unlock()
	if owner != nil && !closed {
		owner.Touch(progress) // existing exact scope only; no reply/allocation
	}
	return true
}

func pullFailure(err error) *proto.Error {
	var wire *proto.Error
	if errors.As(err, &wire) {
		return wire
	}
	return &proto.Error{Code: "pull_failed", Message: "the owned bundle export failed", Hint: "Check the session's runtime and storage, then repeat pull."}
}

func (c *client) closePull() {
	c.pullMu.Lock()
	c.pullClosed = true
	owner := c.pull
	c.pullMu.Unlock()
	if owner != nil {
		owner.Close()
	}
}
