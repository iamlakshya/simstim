package tui

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/client"
)

func (b *fakeBackend) PreviewHostSelection(ctx context.Context, selected string, options client.NewInputOptions) (hostSelectionPreview, error) {
	if err := ctx.Err(); err != nil {
		return hostSelectionPreview{}, err
	}
	b.mu.Lock()
	preview := b.previewHost
	b.mu.Unlock()
	if preview != nil {
		result, err := preview(ctx, selected, options)
		result.names = slices.Clone(result.names)
		return result, err
	}
	host, err := b.ResolveHost(ctx, selected)
	if err != nil {
		return hostSelectionPreview{}, err
	}
	return legacyHostPreview(host, selected), nil
}
