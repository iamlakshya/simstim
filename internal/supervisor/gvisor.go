package supervisor

import (
	"context"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// gvisorSandbox adapts *gvisor.Sandbox (which returns wall.Exit) to the
// supervisor's Sandbox contract and exposes runsc's captured stderr.
type gvisorSandbox struct {
	inner *gvisor.Sandbox
}

func (g *gvisorSandbox) Start(ctx context.Context) (net.Conn, error) { return g.inner.Start(ctx) }
func (g *gvisorSandbox) Stop(ctx context.Context, grace time.Duration) error {
	return g.inner.Stop(ctx, grace)
}
func (g *gvisorSandbox) Kill() error    { return g.inner.Kill() }
func (g *gvisorSandbox) Destroy() error { return g.inner.Destroy() }
func (g *gvisorSandbox) Stderr() string { return g.inner.Stderr() }
func (g *gvisorSandbox) Wait() Exit {
	ex := g.inner.Wait()
	return Exit{Code: ex.Code, Signal: ex.Signal, Err: ex.Err}
}

// NewGVisorSandbox builds an upstream-runsc gVisor-wall Sandbox from m.
func NewGVisorSandbox(id, helperExe string, m *host.Manifest) Sandbox {
	wsHost := m.Cwd
	wsGuest := process.WorkspaceGuestPath(wsHost)
	var stateDirs, cacheDirs []string
	var extraRO, extraRW []gvisor.Mount
	if m.Wall != nil {
		if m.Wall.WorkspaceHost != "" {
			wsHost = m.Wall.WorkspaceHost
		}
		if m.Wall.WorkspaceGuest != "" {
			wsGuest = m.Wall.WorkspaceGuest
		}
		stateDirs = append([]string(nil), m.Wall.StateDirs...)
		cacheDirs = append([]string(nil), m.Wall.CacheDirs...)
		for _, wm := range m.Wall.ExtraRO {
			extraRO = append(extraRO, gvisor.Mount{Source: wm.Source, Dest: wm.Dest, ReadOnly: true})
		}
		for _, wm := range m.Wall.ExtraRW {
			extraRW = append(extraRW, gvisor.Mount{Source: wm.Source, Dest: wm.Dest, ReadOnly: false})
		}
	}
	return &gvisorSandbox{
		inner: gvisor.NewSandbox(gvisor.Spec{
			SessionID:      id,
			SessionDir:     paths.SessionDir(id),
			HelperExe:      helperExe,
			WorkspaceHost:  wsHost,
			WorkspaceGuest: wsGuest,
			StateHost:      paths.SessionStateDir(id),
			StateDirs:      stateDirs,
			CacheHost:      paths.SessionCachesDir(id),
			CacheDirs:      cacheDirs,
			ExtraRO:        extraRO,
			ExtraRW:        extraRW,
			GuestPATH:      envLookup(m.Env, "PATH"),
			ExtraEnv:       []string{brand.Env("GUEST_PROXY") + "=1"},
		}),
	}
}
