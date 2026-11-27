//go:build !linux

package sourcecompose

import (
	"context"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/gitsource"
)

func openPlatformSource(ctx context.Context, path string, graph bool, restrictions []gitsource.Restriction) (client.LocalGitSource, error) {
	// Known-empty applies only after the actual canonical managed inventory
	// has been completely inspected. The intrinsic private-root guard remains.
	return gitsource.Open(ctx, path, graph, func(context.Context) ([]gitsource.Restriction, error) { return nil, nil }, restrictions...)
}
