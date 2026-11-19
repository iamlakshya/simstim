package guest

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type execRun struct {
	fileCommit *fileCommitCommand
	inputBundle *os.File // fixed input Git only; immutable descriptor, never wire argv authority
	vmRole      vmCommandRole
	vmGroup     string // private owned cgroup identity; never a wire/member value
	kind        link.Kind
	purpose     string
	procDone    chan struct{}
	waitOnce    sync.Once
	waitKnown   bool
	waitErr     error
	abortOutput func()
	args        proto.CtlExecArgs
	env         []string
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	taken       bool // protected by Init.mu
	stream      *link.Stream
	mu          sync.Mutex // PID stays owned through the final group signal and reap
	cmd         *exec.Cmd
	gone        bool
}

type execOutput struct {
	records *link.ExecRecords
	channel byte
}

func (w execOutput) Write(p []byte) (int, error) { return w.records.Output(w.channel, p) }

func (g *Init) registerExec(a proto.CtlExecArgs) error {
	g.mu.Lock()
	valid := len(g.execEnv) > 0 && a.Cwd == g.execCwd
	env := append([]string(nil), g.execEnv...)
	g.mu.Unlock()
	if !valid {
		return execError("the sandbox is not ready for commands")
	}
	_, err := g.registerCommand(context.Background(), a, env, link.KindExec, "", true)
	return err
}

func (g *Init) serveExec(st *link.Stream) {
	var h proto.ExecHeader
	if st.Header(&h) != nil {
		st.Close()
		return
	}
	g.mu.Lock()
	run := g.execs[h.ID]
	if run == nil || run.taken || g.ended || run.kind != st.Kind() || run.purpose != h.Purpose {
		g.mu.Unlock()
		st.Close()
		return
	}
	run.taken, run.stream = true, st
	g.mu.Unlock()
	defer func() { _ = st.Close(); g.finishCommand(run) }()
	records := link.NewExecRecords(st)
	code := g.runExec(run, st, records)
	// A slow/disconnected reader cannot retain a command or the init forever.
	_ = st.SetWriteDeadline(time.Now().Add(ctlCloseWait))
	_ = records.End(code)
	_ = st.CloseWrite()
}

func (g *Init) runExec(run *execRun, st *link.Stream, records *link.ExecRecords) int {
	return g.executeOwned(run, ownedCommandIO{
		Out: execOutput{records, link.ExecStdout}, Err: execOutput{records, link.ExecStderr},
		AbortOutput: func() { _ = st.SetWriteDeadline(time.Now().Add(ctlCloseWait)) },
		AttachInput: func(input io.WriteCloser) func() {
			done := make(chan struct{})
			go func() { defer close(done); _ = link.ReadExecInput(st, input, run.signal, run.cancel) }()
			return func() { _ = st.SetReadDeadline(time.Now()); <-done }
		},
	})
}

func (r *execRun) signal(number uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.gone {
		return nil
	}
	return syscall.Kill(-r.cmd.Process.Pid, syscall.Signal(number))
}

func (g *Init) stopExecs() {
	g.stopOwned()
	g.mu.Lock()
	runs := make([]*execRun, 0, len(g.execs))
	for _, run := range g.execs {
		runs = append(runs, run)
		run.cancel()
		if run.stream != nil {
			_ = run.stream.SetDeadline(time.Now())
		}
	}
	g.mu.Unlock()
	for _, run := range runs {
		<-run.done
	}
}

func execCancelled(ctx context.Context) int {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 124
	}
	return 125
}
func execStartCode(err error) int {
	if errors.Is(err, fs.ErrNotExist) {
		return 127
	}
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.ENOEXEC) || errors.Is(err, syscall.EISDIR) {
		return 126
	}
	return 125
}
func execWaitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ws.ExitStatus()
		}
	}
	return 125
}

func execError(message string) error {
	return &proto.Error{Code: "exec_failed", Message: message, Hint: "Check that the sandbox is running, then run the command again."}
}

// guestExecutable resolves only the fixed guest PATH, never exec.LookPath's
// inherited init/host environment. A slash remains relative to guest cwd.
func guestExecutable(name string, env []string, cwd string) (string, int, error) {
	candidates := []string{name}
	if !strings.ContainsRune(name, '/') {
		candidates = nil
		for _, dir := range filepath.SplitList(envValue(env, "PATH")) {
			if dir != "" && filepath.IsAbs(dir) {
				candidates = append(candidates, filepath.Join(dir, name))
			}
		}
	}
	denied := false
	for _, path := range candidates {
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				denied = true
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			denied = true
			continue
		}
		return path, 0, nil
	}
	if denied {
		return "", 126, fs.ErrPermission
	}
	return "", 127, fs.ErrNotExist
}
