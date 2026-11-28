package supervisor

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// processSandbox adapts *process.Sandbox (which returns wall.Exit) to the
// supervisor's Sandbox contract and exposes bubblewrap's captured stderr.
type processSandbox struct {
	inner *process.Sandbox
}

func (p *processSandbox) Start(ctx context.Context) (net.Conn, error) { return p.inner.Start(ctx) }
func (p *processSandbox) Stop(ctx context.Context, g time.Duration) error {
	return p.inner.Stop(ctx, g)
}
func (p *processSandbox) Kill() error    { return p.inner.Kill() }
func (p *processSandbox) Destroy() error { return p.inner.Destroy() }
func (p *processSandbox) Stderr() string { return p.inner.Stderr() }
func (p *processSandbox) Wait() Exit {
	ex := p.inner.Wait()
	return Exit{Code: ex.Code, Signal: ex.Signal, Err: ex.Err}
}

// NewProcessSandbox builds a bubblewrap process-wall Sandbox from m.
func NewProcessSandbox(id, helperExe string, m *host.Manifest) Sandbox {
	wsHost := m.Cwd
	wsGuest := process.WorkspaceGuestPath(wsHost)
	var stateDirs, cacheDirs, keepFiles []string
	var extraRO, extraRW []process.Mount
	shareNet := false
	if m.Wall != nil {
		if m.Wall.WorkspaceHost != "" {
			wsHost = m.Wall.WorkspaceHost
		}
		if m.Wall.WorkspaceGuest != "" {
			wsGuest = m.Wall.WorkspaceGuest
		}
		stateDirs = append([]string(nil), m.Wall.StateDirs...)
		cacheDirs = append([]string(nil), m.Wall.CacheDirs...)
		keepFiles = append([]string(nil), m.Wall.Keep...)
		for _, wm := range m.Wall.ExtraRO {
			extraRO = append(extraRO, process.Mount{Source: wm.Source, Dest: wm.Dest, ReadOnly: true})
		}
		for _, wm := range m.Wall.ExtraRW {
			extraRW = append(extraRW, process.Mount{Source: wm.Source, Dest: wm.Dest, ReadOnly: false})
		}
		shareNet = m.Wall.ShareNet
	}
	var extraEnv []string
	if !shareNet {
		extraEnv = append(extraEnv, brand.Env("GUEST_PROXY")+"=1")
	}
	var keepHost string
	if len(keepFiles) > 0 {
		keepHost = filepath.Join(paths.SessionStateDir(id), "_keep")
	}
	return &processSandbox{
		inner: process.NewSandbox(process.Spec{
			SessionID:      id,
			HelperExe:      helperExe,
			WorkspaceHost:  wsHost,
			WorkspaceGuest: wsGuest,
			StateHost:      paths.SessionStateDir(id),
			StateDirs:      stateDirs,
			CacheHost:      paths.SessionCachesDir(id),
			CacheDirs:      cacheDirs,
			KeepHost:       keepHost,
			KeepFiles:      keepFiles,
			ExtraRO:        extraRO,
			ExtraRW:        extraRW,
			GuestPATH:      envPath(m.Env),
			ExtraEnv:       extraEnv,
			ShareNet:       shareNet,
		}),
	}
}

func envPath(env []string) string {
	for _, kv := range env {
		if val, ok := strings.CutPrefix(kv, "PATH="); ok {
			return val
		}
	}
	return ""
}
