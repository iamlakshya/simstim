package client

import (
	"context"
	"time"
)

// Reconnect retires this host's shared SSH connection before the SAME
// ordinary owned Dial path starts a fresh bridge. Provider transports do
// not use an SSH master and need no retirement.
func Reconnect(ctx context.Context, h Host, interactive bool) (*Conn, time.Duration, error) {
	start := time.Now()
	conn, err := dialWithPreparation(ctx, h, interactive, func(owned context.Context, transport Transport) error {
		if ssh, ok := transport.(*SSH); ok {
			if err := ssh.RetireMaster(owned); err != nil {
				return err
			}
		}
		start = time.Now()
		return nil
	})
	return conn, time.Since(start), err
}
