package cli

import (
	"context"

	"github.com/iamlakshya/simstim/internal/client"
)

// startUnwalledControlV1 uses the caller's original pre-dial request for an
// installer or login. Terminal metadata crosses the namespace check before
// the explicit None choice is resolved, bound and recorded by NewSession.
func startUnwalledControlV1(ctx context.Context, c *client.Conn, a client.NewArgs, cols, rows int) (*client.Session, error) {
	a, err := client.PrepareNewSessionArgs(ctx, a, cols, rows)
	if err != nil {
		return nil, err
	}
	a, err = c.PrepareUnwalledControlStartV1(ctx, a)
	if err != nil {
		return nil, err
	}
	return c.NewSession(ctx, a)
}
