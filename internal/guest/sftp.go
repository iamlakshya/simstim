package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/link"
)

func (r *runner) handleExtraStream(ctx context.Context, st *link.Stream) {
	switch st.Kind() {
	case link.KindSFTP:
		r.serveSFTP(ctx, st)
	case link.KindExec:
		r.serveExec(ctx, st)
	default:
		_ = st.Close()
	}
}

// sessionContext returns the workspace directory and sanitized guest
// environment for sessionID, falling back to the primary session when
// sessionID is empty.
func (r *runner) sessionContext(sessionID string) (cwd string, env []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sessionID != "" {
		if mh, ok := r.holders[sessionID]; ok {
			return mh.cwd, append([]string(nil), mh.env...)
		}
	}
	for _, mh := range r.holders {
		return mh.cwd, append([]string(nil), mh.env...)
	}
	return "", nil
}

// RunSFTPServer serves the SFTP protocol over in and out rooted at workDir.
// Used by the internal "wall-sftp" helper subcommand so microvm SFTP sessions
// run strictly as user agent (uid 1000) rather than root (spec 17.7, 21.1).
func RunSFTPServer(workDir string, in io.Reader, out io.Writer) error {
	var opts []sftp.ServerOption
	if workDir != "" {
		opts = append(opts, sftp.WithServerWorkingDirectory(workDir))
	}
	srv, err := sftp.NewServer(rwcPair{Reader: in, Writer: out}, opts...)
	if err != nil {
		return fmt.Errorf("start guest sftp server: %w", err)
	}
	defer srv.Close()
	if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("serve guest sftp: %w", err)
	}
	return nil
}

type rwcPair struct {
	io.Reader
	io.Writer
}

func (rwcPair) Close() error { return nil }

// serveSFTP serves the SFTP protocol over a link.KindSFTP stream inside the
// sandbox as the guest user (agent), rooted at the session's workspace
// (isolation spec 17.7, 21.1).
func (r *runner) serveSFTP(ctx context.Context, st *link.Stream) {
	defer st.Close()
	var hdr SFTPHeader
	_ = st.Header(&hdr)

	workDir, _ := r.sessionContext(hdr.Session)
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	if r.opts.MicroVM && os.Geteuid() == 0 {
		r.serveSFTPAsAgent(ctx, st, workDir)
		return
	}
	var opts []sftp.ServerOption
	if workDir != "" {
		opts = append(opts, sftp.WithServerWorkingDirectory(workDir))
	}
	srv, err := sftp.NewServer(st, opts...)
	if err != nil {
		return
	}
	defer srv.Close()

	stop := context.AfterFunc(ctx, func() {
		_ = srv.Close()
		_ = st.Close()
	})
	defer stop()

	_ = srv.Serve()
}

func (r *runner) serveSFTPAsAgent(ctx context.Context, st *link.Stream, workDir string) {
	exe := os.Getenv(brand.Env("GUEST_HELPER"))
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return
		}
	}
	//nolint:gosec // G702: exe is wall-init's own read-only helper binary
	cmd := exec.CommandContext(ctx, exe, "wall-sftp", workDir)
	cmd.Dir = workDir
	cmd.Stdin = st
	cmd.Stdout = st
	setAgentExecCredential(cmd, r.agentGroups())

	r.mu.Lock()
	err := cmd.Start()
	var pid int
	if err == nil && cmd.Process != nil {
		pid = cmd.Process.Pid
		r.managed[pid] = struct{}{}
	}
	r.mu.Unlock()
	if err != nil {
		return
	}
	_ = cmd.Wait()
	if pid > 0 {
		r.mu.Lock()
		delete(r.managed, pid)
		r.mu.Unlock()
	}
}
