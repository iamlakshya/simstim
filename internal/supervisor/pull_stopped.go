package supervisor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// BundleSandboxFactory overrides how PullBundle constructs a temporary
// bundle-only sandbox for a stopped walled session. Nil uses the session's
// recorded wall driver. Tests set this to inject an in-process sandbox.
var BundleSandboxFactory func(m *host.Manifest, tmpDir string) (Sandbox, error)

func pullFromStoppedSandbox(m *host.Manifest, req api.PullBundleArgs, out io.Writer) error {
	if err := paths.EnsurePrivateDir(paths.RuntimeDir()); err != nil {
		return writePullBundleErr(out, err)
	}
	tmpDir, err := os.MkdirTemp(paths.RuntimeDir(), "pull-sb-*")
	if err != nil {
		return writePullBundleErr(out, fmt.Errorf("create temp sandbox folder: %w", err))
	}
	defer os.RemoveAll(tmpDir)

	sb, err := buildBundleSandbox(m, tmpDir)
	if err != nil {
		return writePullBundleErr(out, err)
	}

	bootCtx, bootCancel := context.WithTimeout(context.Background(), 20*time.Second)
	conn, err := sb.Start(bootCtx)
	bootCancel()
	if err != nil {
		_ = sb.Destroy()
		return writePullBundleErr(out, host.UserError("sandbox_boot", "could not start bundle sandbox: "+err.Error(), "Check host doctor and try again."))
	}

	sess := link.New(conn, link.Supervisor, link.Options{})
	defer func() {
		_ = sess.Close()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = sb.Stop(stopCtx, 2*time.Second)
		stopCancel()
		_ = sb.Wait()
		_ = sb.Destroy()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), pullStreamTimeout)
	defer cancel()

	head, size, empty, streamErr := streamBundleFromLink(ctx, sess, m, req, out)
	if aud, aerr := openAuditLog(paths.AuditLog(m.ID)); aerr == nil {
		detail := fmt.Sprintf("stopped-sandbox empty=%t size=%d", empty, size)
		if head != "" {
			detail += " head=" + head[:min(len(head), 12)]
		}
		aud.Record(AuditEntry{
			Event:   "pull",
			Session: m.ID,
			Wall:    m.Wall.Level,
			Driver:  m.Wall.Driver,
			Detail:  detail,
		})
		_ = aud.Close()
	}
	return streamErr
}

func buildBundleSandbox(m *host.Manifest, tmpDir string) (Sandbox, error) {
	if BundleSandboxFactory != nil {
		return BundleSandboxFactory(m, tmpDir)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate host helper: %w", err)
	}
	switch m.Wall.Driver {
	case "process":
		return newBundleProcessSandbox(m, exe), nil
	case "gvisor":
		return newBundleGVisorSandbox(m, exe, tmpDir), nil
	case "plain":
		return NewPlainSandbox(m.ID, exe, filepath.Join(tmpDir, "guest")), nil
	default:
		return nil, host.UserError("wall_unavailable",
			fmt.Sprintf("wall driver %q does not support stopped bundle pulls", m.Wall.Driver),
			"Start the session or use a supported wall driver.")
	}
}

func newBundleProcessSandbox(m *host.Manifest, helperExe string) Sandbox {
	wsHost := m.HostDir()
	wsGuest := m.Cwd
	if m.Wall != nil && m.Wall.WorkspaceGuest != "" {
		wsGuest = m.Wall.WorkspaceGuest
	}
	var extraRO, extraRW []process.Mount
	if m.Wall != nil {
		for _, wm := range m.Wall.ExtraRO {
			extraRO = append(extraRO, process.Mount{Source: wm.Source, Dest: wm.Dest, ReadOnly: true})
		}
		for _, wm := range m.Wall.ExtraRW {
			extraRW = append(extraRW, process.Mount{Source: wm.Source, Dest: wm.Dest, ReadOnly: false})
		}
	}
	return &processSandbox{
		inner: process.NewSandbox(process.Spec{
			SessionID:      m.ID,
			HelperExe:      helperExe,
			WorkspaceHost:  wsHost,
			WorkspaceGuest: wsGuest,
			StateHost:      paths.SessionStateDir(m.ID),
			CacheHost:      paths.SessionCachesDir(m.ID),
			ExtraRO:        extraRO,
			ExtraRW:        extraRW,
			GuestPATH:      envPath(m.Env),
			ShareNet:       false,
		}),
	}
}

func newBundleGVisorSandbox(m *host.Manifest, helperExe, tmpDir string) Sandbox {
	wsHost := m.HostDir()
	wsGuest := m.Cwd
	if m.Wall != nil && m.Wall.WorkspaceGuest != "" {
		wsGuest = m.Wall.WorkspaceGuest
	}
	var extraRO, extraRW []gvisor.Mount
	if m.Wall != nil {
		for _, wm := range m.Wall.ExtraRO {
			extraRO = append(extraRO, gvisor.Mount{Source: wm.Source, Dest: wm.Dest, ReadOnly: true})
		}
		for _, wm := range m.Wall.ExtraRW {
			extraRW = append(extraRW, gvisor.Mount{Source: wm.Source, Dest: wm.Dest, ReadOnly: false})
		}
	}
	return &gvisorSandbox{
		inner: gvisor.NewSandbox(gvisor.Spec{
			SessionID:      m.ID,
			SessionDir:     tmpDir,
			HelperExe:      helperExe,
			WorkspaceHost:  wsHost,
			WorkspaceGuest: wsGuest,
			StateHost:      paths.SessionStateDir(m.ID),
			CacheHost:      paths.SessionCachesDir(m.ID),
			ExtraRO:        extraRO,
			ExtraRW:        extraRW,
			GuestPATH:      envPath(m.Env),
		}),
	}
}
