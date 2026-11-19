package tui

import (
	"context"

	"github.com/iamlakshya/simstim/internal/client"
)

func (b *fakeBackend) ResolveHost(ctx context.Context, selected string) (client.Host, error) {
	if err := ctx.Err(); err != nil {
		return client.Host{}, err
	}
	b.mu.Lock()
	resolve, hosts := b.resolveHost, append([]client.Host(nil), b.hosts...)
	b.mu.Unlock()
	if resolve != nil {
		return resolve(ctx, selected)
	}
	for _, host := range hosts {
		if host.Name == selected && client.ValidHostName(selected) {
			return host, nil
		}
	}
	if selected != "" {
		return client.Host{}, &client.UserError{Code: "host_not_found", Kind: client.KindNotFound,
			Msg: "the selected fixture host is no longer saved", Hint: "Refresh hosts and choose again."}
	}
	return client.Host{}, &client.UserError{Code: "host_required", Kind: client.KindUsage,
		Msg: "the fixture has no automatic host selection", Hint: "Choose a host in the list."}
}
