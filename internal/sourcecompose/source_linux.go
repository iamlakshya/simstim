//go:build linux

// Package sourcecompose wires the one readonly source authority at application
// entrypoints. Keeping it outside client avoids host-test import cycles.
package sourcecompose

import (
	"context"
	"sync"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
)

var install struct {
	sync.Once
	err error
}

// Install fixes the canonical Linux provider once before CLI or TUI work.
func Install() error {
	install.Do(func() {
		install.err = client.InstallLocalGitSources(func(ctx context.Context, path string, graph bool) (client.LocalGitSource, error) {
			return host.OpenGitSourceCapability(ctx, path, graph)
		}, client.OpenLocalGitMetadataSource)
	})
	return install.err
}
