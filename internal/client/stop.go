package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// Stop asks a session's supervisor to stop its sandbox and retain its files.
func (c *Conn) Stop(ctx context.Context, id string) (*Session, error) {
	if !c.HasFeature("session.stop") {
		return nil, &UserError{Msg: "this host helper cannot stop sandboxes", Hint: "Update the host helper, then try again.", Code: "helper_old"}
	}
	var row api.SessionInfo
	if err := c.Call(ctx, "stop", map[string]string{"id": id}, &row); err != nil {
		return nil, err
	}
	if err := checkSessionID(c.Host.Name, row.ID); err != nil {
		return nil, err
	}
	if row.ID != id {
		return nil, &UserError{Msg: "the host answered for a different session", Hint: "List sessions and retry the exact session you selected.", Code: "protocol"}
	}
	return &row, nil
}
