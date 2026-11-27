//go:build linux

// Package sourcecompose wires the one readonly source authority at application
// entrypoints. Keeping it outside client avoids host-test import cycles.
package sourcecompose

import (
	"context"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/gitsource"
	"github.com/iamlakshya/simstim/internal/host"
)

func openPlatformSource(ctx context.Context, path string, graph bool, restrictions []gitsource.Restriction) (client.LocalGitSource, error) {
	extra := make([]host.GitSourceRestriction, 0, len(restrictions))
	for _, r := range restrictions {
		extra = append(extra, host.GitSourceRestriction{Path: r.Path, Identity: r.Identity})
	}
	return host.OpenGitSourceCapabilityRestricted(ctx, path, graph, extra...)
}
