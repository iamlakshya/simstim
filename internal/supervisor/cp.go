package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, proto.FeatureFileTransferV1, "cp") }

func copyRefusal(code, message string) *proto.Error {
	return &proto.Error{Code: code, Message: message, Hint: "Copy ordinary paths from the running sandbox; use pull for work in Git."}
}

func (s *Supervisor) copyRequest(c *client, req proto.Request) bool {
	if req.Op != "cp.list" && req.Op != "cp.read" {
		return false
	}
	c.copyMu.Lock()
	if c.copyBusy {
		c.copyMu.Unlock()
		c.reply(req.ID, nil, copyRefusal("cp_busy", "the previous copy request has not joined"))
		return true
	}
	c.copyBusy = true
	c.copyMu.Unlock()
	root := s.owner()
	ctx, finish, err := root.beginFileTransfer(c)
	if err != nil {
		c.copyMu.Lock()
		c.copyBusy = false
		c.copyMu.Unlock()
		c.reply(req.ID, nil, copyRefusal("sandbox_stopped", "the sandbox is not running"))
		return true
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		defer finish()
		defer cancel()
		var value any
		var err error
		c.copyMu.Lock()
		reader := c.copyPlan
		c.copyMu.Unlock()
		if reader == nil {
			var limit int64
			limit, err = root.copyPolicyLimit()
			if err == nil {
				reader, err = filecopy.NewReadSessionV1(s.id, limit, func(ctx context.Context, header proto.FileGetHeaderV1) ([]byte, error) {
					return filecopy.ReadGuestV1(ctx, root.link.Open, header)
				}, func(selected string, size int64) {
					root.audit.write("cp_out", map[string]any{"path": selected, "size": size})
				})
			}
			if err == nil {
				c.copyMu.Lock()
				c.copyPlan = reader
				c.copyMu.Unlock()
			}
		}
		if err == nil {
			if req.Op == "cp.list" {
				value, err = reader.List(ctx, req.Args)
			} else {
				value, err = reader.Read(ctx, req.Args)
			}
		}
		c.copyMu.Lock()
		c.copyBusy = false
		c.copyMu.Unlock()
		if err != nil {
			c.reply(req.ID, nil, toProtoErr(filecopy.ReadErrorV1(err)))
		} else {
			c.reply(req.ID, value, nil)
		}
	}()
	return true
}

func (s *Supervisor) copyPolicyLimit() (int64, error) {
	s.mu.Lock()
	var policy *api.Policy
	if s.m.Policy != nil {
		clone := api.ClonePolicy(*s.m.Policy)
		policy = &clone
	}
	s.mu.Unlock()
	return filecopy.CopyMax(policy)
}
