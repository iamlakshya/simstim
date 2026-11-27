package tui

import (
	"context"

	"github.com/iamlakshya/simstim/internal/client"
)

func (realBackend) ResolveHost(ctx context.Context, selected string) (client.Host, error) {
	return client.ResolveHost(ctx, selected)
}

func (b *demoBackend) ResolveHost(ctx context.Context, selected string) (client.Host, error) {
	if err := ctx.Err(); err != nil {
		return client.Host{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, host := range b.hosts {
		if host.Name == selected && client.ValidHostName(selected) {
			return host, nil
		}
	}
	return client.Host{}, &client.UserError{Code: "host_required", Kind: client.KindUsage,
		Msg: "the demo has no default host", Hint: "Choose a host in the list."}
}

func (c *demoConn) CheckSavedSource(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return client.ErrClosed
	default:
		return nil
	}
}
