// Package sourcecompose wires the one read-only source authority at actual
// application entrypoints without a client-to-host import cycle.
package sourcecompose

import (
	"sync"

	"github.com/iamlakshya/simstim/internal/client"
)

var install struct {
	sync.Once
	err error
}

// Install fixes both real source dependencies once before CLI or TUI work.
func Install() error {
	install.Do(func() { install.err = client.InstallLocalGitSources(openManaged, client.OpenLocalGitMetadataSource) })
	return install.err
}
