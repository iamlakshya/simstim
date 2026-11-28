package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Output stream tags carried in the first byte of proto.TOutput frames for
// ssh-exec (matching spec 10.2's exec stream: 1 stdout, 2 stderr).
const (
	SSHStreamStdout byte = 1
	SSHStreamStderr byte = 2
)

// execProcess serializes process-group signals against reaping so no signal
// can race with cmd.Wait recycling the PID.
type execProcess struct {
	mu     sync.Mutex
	proc   *os.Process
	reaped bool
}

func (p *execProcess) markReaping() {
	p.mu.Lock()
	p.reaped = true
	p.mu.Unlock()
}

func (p *execProcess) killGroup() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reaped || p.proc == nil || p.proc.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-p.proc.Pid, syscall.SIGKILL)
}

func (p *execProcess) signalPayload(payload []byte) {
	sig, ok := parseSSHSignal(payload)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reaped || p.proc == nil || p.proc.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-p.proc.Pid, sig)
}

// RunSSHExec runs one command or an ephemeral interactive shell inside session
// id's workspace for ssh-proxy (spec 17.7). It speaks proto frames over in and
// out so that stdout, stderr, stdin EOF, window resizes, signals and the exit
// status stay separate over any Transport.
func RunSSHExec(id string, in io.Reader, out io.Writer) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if !validID(id) {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	m, err := LoadManifest(id)
	if err != nil {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	if isWalled(m) {
		return UserError("forbidden",
			fmt.Sprintf("session %s runs inside a wall, so host ssh-exec is refused", id),
			"Update the client so exec runs inside the sandbox through the supervisor.")
	}
	r := bufio.NewReaderSize(in, 64<<10)
	w := proto.NewWriter(out)
	req, args, err := readSSHExecStart(r, w)
	if err != nil {
		return err
	}

	login, err := LoginEnv(10 * time.Second)
	if err != nil {
		login = os.Environ()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := buildSSHExecCmd(ctx, m, login, args)
	return ServeSSHExecCmd(cmd, cancel, req.ID, args, in, r, w, ExecHooks{})
}

// ExecHooks lets guest init coordinate child PID tracking with its PID 1
// reaper and close the underlying link stream once TEnded is written.
type ExecHooks struct {
	Start    func(start func() error) error
	Reaped   func(pid int)
	AfterEnd func()
}

// ServeSSHExecCmd runs cmd with PTY or pipe framing over r and w using the
// ssh-exec proto stream format (used on unwalled hosts and inside guest init).
func ServeSSHExecCmd(cmd *exec.Cmd, cancel context.CancelFunc, reqID uint64, args api.SSHExecArgs, rawIn io.Reader, r *bufio.Reader, w *proto.Writer, hooks ExecHooks) error {
	if args.PTY {
		return runSSHExecPTY(cmd, cancel, reqID, args, rawIn, r, w, hooks)
	}
	return runSSHExecPipe(cmd, cancel, reqID, rawIn, r, w, hooks)
}

func readSSHExecStart(r *bufio.Reader, w *proto.Writer) (proto.Request, api.SSHExecArgs, error) {
	f, err := proto.Read(r)
	if err != nil {
		return proto.Request{}, api.SSHExecArgs{}, err
	}
	if f.Type != proto.TRequest {
		return proto.Request{}, api.SSHExecArgs{}, errors.New("ssh-exec: expected start request")
	}
	var req proto.Request
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return proto.Request{}, api.SSHExecArgs{}, fmt.Errorf("ssh-exec: decode request: %w", err)
	}
	var args api.SSHExecArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: false, Error: UserError("bad_request", err.Error(), "Update the client.")})
			return proto.Request{}, api.SSHExecArgs{}, fmt.Errorf("ssh-exec: decode args: %w", err)
		}
	}
	return req, args, nil
}

func buildSSHExecCmd(ctx context.Context, m *Manifest, login []string, args api.SSHExecArgs) *exec.Cmd {
	shell := LoginShell()
	if shell == "" {
		shell = "/bin/sh"
	}
	var cmd *exec.Cmd
	if args.Command != "" {
		cmd = exec.CommandContext(ctx, shell, "-c", args.Command)
	} else {
		cmd = exec.CommandContext(ctx, shell, "-l")
	}
	workDir := m.Cwd
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}
	cmd.Dir = workDir
	extra := map[string]string{}
	if args.Term != "" {
		extra["TERM"] = args.Term
	}
	cmd.Env = AgentEnv(login, extra)
	return cmd
}

type ptyHandle struct {
	mu     sync.Mutex
	f      *os.File
	closed bool
}

func (p *ptyHandle) write(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		_, _ = p.f.Write(b)
	}
}

func (p *ptyHandle) resize(cols, rows uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		_ = pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
	}
}

func (p *ptyHandle) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	_ = p.f.Close()
}

func runSSHExecPTY(cmd *exec.Cmd, cancel context.CancelFunc, reqID uint64, args api.SSHExecArgs, rawIn io.Reader, r *bufio.Reader, w *proto.Writer, hooks ExecHooks) error {
	cols, rows := clampExecSize(args.Cols, args.Rows)
	var ptmx *os.File
	start := func() error {
		var serr error
		ptmx, serr = startSSHExecPTY(cmd, &pty.Winsize{Cols: cols, Rows: rows})
		if serr != nil {
			return fmt.Errorf("start ssh-exec PTY: %w", serr)
		}
		return nil
	}
	var err error
	if hooks.Start != nil {
		err = hooks.Start(start)
	} else {
		err = start()
	}
	if err != nil {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: reqID, OK: false, Error: UserError("exec_failed", err.Error(), "Check the command and try again.")})
		return err
	}
	pid := cmd.Process.Pid
	ph := &ptyHandle{f: ptmx}
	defer ph.close()
	ep := &execProcess{proc: cmd.Process}
	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: reqID, OK: true}); err != nil {
		ep.killGroup()
		cancel()
		_ = cmd.Wait()
		if hooks.Reaped != nil {
			hooks.Reaped(pid)
		}
		return err
	}

	outDone := make(chan struct{})
	go pumpSSHStream(ptmx, w, SSHStreamStdout, outDone)

	inDone := make(chan struct{})
	go pumpSSHPTYInput(r, ph, ep, cancel, inDone)

	waitErr := waitExecProcess(cmd, ep)
	if hooks.Reaped != nil {
		hooks.Reaped(pid)
	}
	drainPTYOutput(outDone, ph)
	err = w.WriteJSON(proto.TEnded, endedFromWait(waitErr))
	finishExecInput(rawIn, inDone, hooks.AfterEnd)
	return err
}

func drainPTYOutput(outDone <-chan struct{}, ph *ptyHandle) {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-outDone:
	case <-timer.C:
		ph.close()
		<-outDone
	}
	ph.close()
}

func pumpSSHPTYInput(r *bufio.Reader, ph *ptyHandle, ep *execProcess, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	for {
		f, rerr := proto.Read(r)
		if rerr != nil {
			ep.killGroup()
			cancel()
			return
		}
		switch f.Type {
		case proto.TInput:
			if len(f.Payload) > 0 {
				ph.write(f.Payload)
			}
		case proto.TResize:
			resizeSSHPTY(ph, f.Payload)
		case proto.TEvent:
			ep.signalPayload(f.Payload)
		case proto.TDetach:
			ep.killGroup()
			cancel()
			return
		default:
		}
	}
}

func resizeSSHPTY(ph *ptyHandle, payload []byte) {
	var rs proto.Resize
	if json.Unmarshal(payload, &rs) != nil || rs.Cols <= 0 || rs.Rows <= 0 {
		return
	}
	cols, rows := clampExecSize(rs.Cols, rs.Rows)
	ph.resize(cols, rows)
}

func clampExecSize(cols, rows int) (uint16, uint16) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return clampCols(cols), clampRows(rows)
}

func runSSHExecPipe(cmd *exec.Cmd, cancel context.CancelFunc, reqID uint64, rawIn io.Reader, r *bufio.Reader, w *proto.Writer, hooks ExecHooks) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	stdin, stdout, stderr, err := openExecPipes(cmd, reqID, w)
	if err != nil {
		return err
	}
	if hooks.Start != nil {
		err = hooks.Start(cmd.Start)
	} else {
		err = cmd.Start()
	}
	if err != nil {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: reqID, OK: false, Error: UserError("exec_failed", err.Error(), "Check the command and try again.")})
		return fmt.Errorf("start ssh-exec command: %w", err)
	}
	pid := cmd.Process.Pid
	ep := &execProcess{proc: cmd.Process}
	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: reqID, OK: true}); err != nil {
		ep.killGroup()
		cancel()
		_ = cmd.Wait()
		if hooks.Reaped != nil {
			hooks.Reaped(pid)
		}
		return err
	}

	outDone := make(chan struct{})
	errDone := make(chan struct{})
	go pumpSSHStream(stdout, w, SSHStreamStdout, outDone)
	go pumpSSHStream(stderr, w, SSHStreamStderr, errDone)

	inDone := make(chan struct{})
	go pumpSSHPipeInput(r, stdin, ep, cancel, inDone)

	<-outDone
	<-errDone
	waitErr := waitExecProcess(cmd, ep)
	if hooks.Reaped != nil {
		hooks.Reaped(pid)
	}
	err = w.WriteJSON(proto.TEnded, endedFromWait(waitErr))
	finishExecInput(rawIn, inDone, hooks.AfterEnd)
	return err
}

func openExecPipes(cmd *exec.Cmd, reqID uint64, w *proto.Writer) (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: reqID, OK: false, Error: UserError("exec_failed", err.Error(), "Try again.")})
		return nil, nil, nil, fmt.Errorf("open ssh-exec stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: reqID, OK: false, Error: UserError("exec_failed", err.Error(), "Try again.")})
		return nil, nil, nil, fmt.Errorf("open ssh-exec stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: reqID, OK: false, Error: UserError("exec_failed", err.Error(), "Try again.")})
		return nil, nil, nil, fmt.Errorf("open ssh-exec stderr: %w", err)
	}
	return stdin, stdout, stderr, nil
}

func pumpSSHPipeInput(r *bufio.Reader, stdin io.WriteCloser, ep *execProcess, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	defer stdin.Close()
	for {
		f, rerr := proto.Read(r)
		if rerr != nil {
			_ = stdin.Close()
			ep.killGroup()
			cancel()
			return
		}
		switch f.Type {
		case proto.TInput:
			if len(f.Payload) == 0 {
				_ = stdin.Close()
				continue
			}
			_, _ = stdin.Write(f.Payload)
		case proto.TDetach:
			_ = stdin.Close()
		case proto.TEvent:
			ep.signalPayload(f.Payload)
		default:
		}
	}
}

func pumpSSHStream(src io.Reader, w *proto.Writer, tag byte, done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			_ = writeTaggedOutput(w, tag, buf[:n])
		}
		if rerr != nil {
			return
		}
	}
}

func waitExecProcess(cmd *exec.Cmd, ep *execProcess) error {
	if cmd.Process != nil {
		waitBeforeReap(cmd.Process.Pid)
	}
	ep.markReaping()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("wait ssh-exec process: %w", err)
	}
	return nil
}

func finishExecInput(rawIn io.Reader, inDone <-chan struct{}, afterEnd func()) {
	if afterEnd != nil {
		afterEnd()
		<-inDone
		return
	}
	if pr, ok := rawIn.(*io.PipeReader); ok {
		_ = pr.Close()
		<-inDone
	}
}

func writeTaggedOutput(w *proto.Writer, tag byte, data []byte) error {
	payload := make([]byte, 1+len(data))
	payload[0] = tag
	copy(payload[1:], data)
	return w.Write(proto.TOutput, payload)
}

func endedFromWait(err error) proto.Ended {
	if err == nil {
		return proto.Ended{ExitCode: 0}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			sig := ws.Signal()
			return proto.Ended{ExitCode: 128 + int(sig), Signal: sig.String()}
		}
		return proto.Ended{ExitCode: ee.ExitCode()}
	}
	return proto.Ended{ExitCode: 1}
}

func parseSSHSignal(payload []byte) (syscall.Signal, bool) {
	var ev api.SSHSignalEvent
	if json.Unmarshal(payload, &ev) != nil || ev.Signal == "" {
		return 0, false
	}
	name := strings.TrimPrefix(strings.ToUpper(ev.Signal), "SIG")
	switch name {
	case "INT":
		return syscall.SIGINT, true
	case "TERM":
		return syscall.SIGTERM, true
	case "HUP":
		return syscall.SIGHUP, true
	case "KILL":
		return syscall.SIGKILL, true
	case "QUIT":
		return syscall.SIGQUIT, true
	case "ALRM":
		return syscall.SIGALRM, true
	case "USR1":
		return syscall.SIGUSR1, true
	case "USR2":
		return syscall.SIGUSR2, true
	default:
		return 0, false
	}
}
