package guest

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// serveExec runs an ssh-proxy command or ephemeral shell inside the sandbox
// over a link.KindExec stream using the session's workspace and guest
// environment (isolation spec 10.5, 17.7).
func (r *runner) serveExec(ctx context.Context, st *link.Stream) {
	defer st.Close()
	var hdr ExecHeader
	if err := st.Header(&hdr); err != nil {
		return
	}
	var args api.SSHExecArgs
	if hdr.SSH != nil {
		args = *hdr.SSH
	}
	workDir, env := r.sessionContext(hdr.Session)
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	if len(env) == 0 {
		env = os.Environ()
	}
	if args.Term != "" {
		env = withEnvVar(env, "TERM", args.Term)
	}

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		cancel()
		_ = st.Close()
	})
	defer stop()

	shell := guestExecShell(env)
	var cmd *exec.Cmd
	if args.Command != "" {
		cmd = exec.CommandContext(execCtx, shell, "-c", args.Command)
	} else {
		cmd = exec.CommandContext(execCtx, shell)
	}
	cmd.Dir = workDir
	cmd.Env = env
	if r.opts.MicroVM {
		setAgentExecCredential(cmd, r.agentGroups())
	}

	hooks := host.ExecHooks{
		Start: func(start func() error) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if err := start(); err != nil {
				return err
			}
			if cmd.Process != nil {
				r.managed[cmd.Process.Pid] = struct{}{}
				if r.opts.MicroVM {
					placePIDInAgentCgroup(r.agentCgroupDir(), cmd.Process.Pid)
				}
			}
			return nil
		},
		Reaped: func(pid int) {
			r.mu.Lock()
			delete(r.managed, pid)
			r.mu.Unlock()
		},
		AfterEnd: func() {
			_ = st.Close()
		},
	}

	_ = host.ServeSSHExecCmd(cmd, cancel, 1, args, st, bufio.NewReaderSize(st, 64<<10), proto.NewWriter(st), hooks)
}

func guestExecShell(env []string) string {
	for _, kv := range env {
		if val, ok := strings.CutPrefix(kv, "SHELL="); ok && val != "" {
			if fi, err := os.Stat(val); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
				return val
			}
		}
	}
	return "/bin/sh"
}
