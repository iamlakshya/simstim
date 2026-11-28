package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// StopArgs asks a host to stop a session while keeping its state on disk.
type StopArgs = api.StopArgs

// StopResult is what a stop did: the session's updated row, and whether
// it was already stopped.
type StopResult = api.StopResult

// Stop stops a session on the host and keeps its folder, manifest, worktree
// and conversation for a later resume or restore.
func (c *Conn) Stop(ctx context.Context, id string) (*StopResult, error) {
	if !c.HasFeature("stop") {
		return nil, c.tooOldToStop()
	}
	var r StopResult
	if err := c.Call(ctx, "stop", StopArgs{ID: id}, &r); err != nil {
		return nil, c.stopError(err)
	}
	if err := checkSessionID(c.Host.Name, r.Session.ID); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Conn) stopError(err error) error {
	var ue *UserError
	if errors.As(err, &ue) && ue.Code == "unknown_op" {
		return c.tooOldToStop()
	}
	return err
}

func (c *Conn) tooOldToStop() error {
	return &UserError{Msg: "the helper on " + c.Host.Name + " is too old to stop a session",
		Hint: brand.Name + " host add " + c.Host.Name + " (updates the helper)", Code: "helper_old"}
}
