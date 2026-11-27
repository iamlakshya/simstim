package tui

import (
	"context"

	"github.com/iamlakshya/simstim/internal/client"
)

// This is a modelled boundary for UI routing tests, never transport provenance.
func (c *fakeConn) CheckSavedSource(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return client.ErrClosed
	}
	return c.savedSourceErr
}
