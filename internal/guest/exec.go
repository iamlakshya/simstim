package guest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type pendingExec struct {
	req     ExecReq
	created time.Time
}

func (r *runner) registerExec(a ExecReq) *proto.Error {
	if a.ID == "" || len(a.Argv) == 0 || strings.TrimSpace(a.Argv[0]) == "" {
		return &proto.Error{Code: "bad_request", Message: "exec requires id and argv", Hint: "Pass a command after --."}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for id, p := range r.execs {
		if now.Sub(p.created) > pendingExecTTL {
			delete(r.execs, id)
		}
	}
	if len(r.execs) >= maxPendingExecs {
		return &proto.Error{Code: "busy", Message: "too many pending exec requests", Hint: "Wait for an existing command to start and try again."}
	}
	if _, dup := r.execs[a.ID]; dup {
		return &proto.Error{Code: "bad_request", Message: "duplicate exec id " + a.ID, Hint: "Try again."}
	}
	r.execs[a.ID] = &pendingExec{req: a, created: now}
	return nil
}

func (r *runner) takePendingExec(id string) (ExecReq, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.execs[id]
	if !ok {
		return ExecReq{}, false
	}
	delete(r.execs, id)
	return p.req, true
}

// serveExec dispatches a link.KindExec stream to either the ssh-proxy handler
// (Mode == "ssh" / SSH args set, spec 10.5 & 17.7) or a registered ctl exec
// command using spec 10.2 framing (tag 1 stdout, tag 2 stderr, tag 0 exit).
func (r *runner) serveExec(ctx context.Context, st *link.Stream) {
	defer st.Close()
	var hdr ExecHeader
	if err := st.Header(&hdr); err != nil {
		return
	}
	if hdr.Mode == "ssh" || hdr.SSH != nil {
		r.serveSSHExec(ctx, st, hdr)
		return
	}
	r.serveCtlExec(ctx, st, hdr)
}

// serveSSHExec runs an ssh-proxy command or ephemeral shell inside the sandbox
// over a link.KindExec stream using the session's workspace and guest
// environment (isolation spec 10.5, 17.7).
func (r *runner) serveSSHExec(ctx context.Context, st *link.Stream, hdr ExecHeader) {
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

// serveCtlExec runs one registered non-interactive command over a link.KindExec
// stream using the spec 10.2 framing (tag 1 stdout, tag 2 stderr, tag 0 exit).
func (r *runner) serveCtlExec(ctx context.Context, st *link.Stream, hdr ExecHeader) {
	if hdr.ID == "" {
		_ = api.WriteExecExit(st, 125)
		return
	}
	req, ok := r.takePendingExec(hdr.ID)
	if !ok {
		_ = api.WriteExecChunk(st, api.ExecRecordStderr, []byte("exec request not registered\n"))
		_ = api.WriteExecExit(st, 125)
		return
	}

	var writeMu sync.Mutex
	writeChunk := func(kind byte, data []byte) {
		writeMu.Lock()
		_ = api.WriteExecChunk(st, kind, data)
		writeMu.Unlock()
	}
	writeExit := func(code int) {
		writeMu.Lock()
		_ = api.WriteExecExit(st, code)
		writeMu.Unlock()
	}

	env := r.execBaseEnv(req.Env)
	exePath, code, err := resolveExecBinary(req.Argv[0], envPath(env))
	if err != nil {
		writeChunk(api.ExecRecordStderr, []byte(fmt.Sprintf("%s: %v\n", req.Argv[0], err)))
		writeExit(code)
		return
	}

	cwd := req.Cwd
	if cwd == "" {
		r.mu.Lock()
		cwd = r.baseCwd
		r.mu.Unlock()
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), exePath, req.Argv[1:]...)
	cmd.Args = append([]string{req.Argv[0]}, req.Argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
	setExecSysProcAttr(cmd)
	if r.opts.MicroVM {
		setAgentExecCredential(cmd, r.agentGroups())
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		writeChunk(api.ExecRecordStderr, []byte(err.Error()+"\n"))
		writeExit(125)
		return
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		writeChunk(api.ExecRecordStderr, []byte(err.Error()+"\n"))
		writeExit(125)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		writeChunk(api.ExecRecordStderr, []byte(err.Error()+"\n"))
		writeExit(125)
		return
	}

	// Register the child PID under r.mu before unlocking so the PID 1 reaper
	// never reaps it out from under cmd.Wait().
	r.mu.Lock()
	startErr := cmd.Start()
	if startErr == nil {
		r.managed[cmd.Process.Pid] = struct{}{}
		if r.opts.MicroVM {
			placePIDInAgentCgroup(r.agentCgroupDir(), cmd.Process.Pid)
		}
	}
	r.mu.Unlock()
	if startErr != nil {
		_ = stdinPipe.Close()
		writeChunk(api.ExecRecordStderr, []byte(fmt.Sprintf("%s: %v\n", req.Argv[0], startErr)))
		writeExit(classifyExecStartErr(startErr))
		return
	}
	pid := cmd.Process.Pid

	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()

	// Forward stdin from the stream into the child; CloseWrite from the client
	// cleanly closes stdin without killing the running command.
	go func() {
		_, _ = io.Copy(stdinPipe, st)
		_ = stdinPipe.Close()
	}()

	var timedOut, peerGone atomic.Bool
	reaped := make(chan struct{})
	watchDone := r.watchExecControl(ctx, execCtx, st, pid, req.TimeoutMS, reaped, &timedOut, &peerGone)

	var outWG sync.WaitGroup
	outWG.Add(2)
	streamPipe := func(pipe io.ReadCloser, kind byte) {
		defer outWG.Done()
		buf := make([]byte, 32<<10)
		for {
			n, rerr := pipe.Read(buf)
			if n > 0 {
				writeChunk(kind, buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}
	go streamPipe(stdoutPipe, api.ExecRecordStdout)
	go streamPipe(stderrPipe, api.ExecRecordStderr)

	outWG.Wait()
	waitErr := cmd.Wait()
	close(reaped)
	r.mu.Lock()
	delete(r.managed, pid)
	r.mu.Unlock()

	cancelExec()
	<-watchDone

	if peerGone.Load() {
		return
	}
	code = 0
	switch {
	case timedOut.Load():
		code = 124
	case waitErr != nil:
		code = exitCodeFromWaitErr(waitErr)
	}
	writeExit(code)
}

func (r *runner) watchExecControl(
	parentCtx, execCtx context.Context,
	st *link.Stream,
	pid int,
	timeoutMS int64,
	reaped <-chan struct{},
	timedOut, peerGone *atomic.Bool,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		peerCh := make(chan bool, 1)
		go func() {
			peerCh <- st.WaitPeerGone(execCtx)
		}()
		var timeoutCh <-chan time.Time
		if timeoutMS > 0 {
			timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
			defer timer.Stop()
			timeoutCh = timer.C
		}
		select {
		case <-execCtx.Done():
			if parentCtx.Err() != nil {
				killExecGroup(pid, reaped)
			}
		case <-timeoutCh:
			timedOut.Store(true)
			killExecGroup(pid, reaped)
		case gone := <-peerCh:
			if gone {
				peerGone.Store(true)
				killExecGroup(pid, reaped)
			}
		}
	}()
	return done
}

func envPath(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			return v
		}
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

func resolveExecBinary(name, pathEnv string) (string, int, error) {
	if strings.Contains(name, "/") {
		fi, err := os.Stat(name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", 127, fmt.Errorf("stat %s: %w", name, err)
			}
			return "", 126, fmt.Errorf("stat %s: %w", name, err)
		}
		if fi.IsDir() {
			return "", 126, errors.New("is a directory")
		}
		if fi.Mode()&0o111 == 0 {
			return "", 126, os.ErrPermission
		}
		return name, 0, nil
	}
	var permErr error
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		cand := filepath.Join(dir, name)
		fi, err := os.Stat(cand)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			permErr = errors.New("is a directory")
			continue
		}
		if fi.Mode()&0o111 == 0 {
			permErr = os.ErrPermission
			continue
		}
		return cand, 0, nil
	}
	if permErr != nil {
		return "", 126, permErr
	}
	return "", 127, exec.ErrNotFound
}
