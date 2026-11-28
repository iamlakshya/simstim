// Package supervisor is the trusted side of one walled session
// (<helper> supervise <id>). It owns the sandbox, the session's trusted
// manifest, the audit log, alerts, unit memory tracking and the session socket
// (paths.Socket). It never runs, renders or interpolates guest data and never
// inspects or mutates terminal output payloads.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Exit describes how a sandbox process ended.
type Exit struct {
	Code   int
	Signal string
	Err    error
}

// Sandbox is the minimal wall boundary contract between the supervisor and a
// sandbox driver (matching spec 5.1's wall.Sandbox).
type Sandbox interface {
	// Start starts the sandbox and returns the supervisor end of the link.
	Start(ctx context.Context) (net.Conn, error)
	// Stop waits up to grace for a clean exit, then kills the sandbox.
	Stop(ctx context.Context, grace time.Duration) error
	// Kill stops the sandbox immediately.
	Kill() error
	// Wait blocks until the sandbox exits and returns how it ended.
	Wait() Exit
	// Destroy removes temporary sandbox runtime files after Wait returns.
	Destroy() error
}

// plainSandbox is the test-only unwalled driver ("plain", spec 5.1). It starts
// `<helper> wall-init` as a direct child with no namespaces, connected via a
// Unix socket pair inherited on fd 3. Neither side opens a network port, and
// the trusted side opens no listener for the guest.
type plainSandbox struct {
	id        string
	helperExe string
	runDir    string

	mu      sync.Mutex
	started bool
	cmd     *exec.Cmd
	done    chan struct{}
	exit    Exit
}

// NewPlainSandbox builds a test-only plain driver for session id.
func NewPlainSandbox(id, helperExe, runDir string) Sandbox {
	if runDir == "" {
		runDir = paths.GuestRuntimeDir(id)
	}
	return &plainSandbox{
		id:        id,
		helperExe: helperExe,
		runDir:    runDir,
		done:      make(chan struct{}),
	}
}

func (p *plainSandbox) Start(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil, errors.New("sandbox already started. Try: stop the session first")
	}
	if err := paths.EnsurePrivateDir(p.runDir); err != nil {
		return nil, err
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("create link socket pair: %w. Try: check open file limits on the host", err)
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	hostFile := os.NewFile(uintptr(fds[0]), "link-supervisor")
	guestFile := os.NewFile(uintptr(fds[1]), "link-guest")

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = hostFile.Close()
		_ = guestFile.Close()
		return nil, fmt.Errorf("open /dev/null: %w", err)
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), p.helperExe, "wall-init")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.ExtraFiles = []*os.File{guestFile}
	cmd.Env = plainGuestEnv(p.runDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	startErr := cmd.Start()
	_ = devnull.Close()
	_ = guestFile.Close()
	if startErr != nil {
		_ = hostFile.Close()
		return nil, fmt.Errorf("start guest init: %w. Try: check the host helper installation", startErr)
	}

	conn, err := net.FileConn(hostFile)
	_ = hostFile.Close()
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("wrap link socket: %w", err)
	}

	p.started = true
	p.cmd = cmd
	go p.reap(cmd)
	return conn, nil
}

func (p *plainSandbox) reap(cmd *exec.Cmd) {
	err := cmd.Wait()
	ex := exitFrom(err)
	p.mu.Lock()
	p.exit = ex
	p.mu.Unlock()
	close(p.done)
}

func (p *plainSandbox) Stop(ctx context.Context, grace time.Duration) error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if grace <= 0 {
		grace = 5 * time.Second
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
		return p.Kill()
	case <-ctx.Done():
		return p.Kill()
	}
}

func (p *plainSandbox) Kill() error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-p.done
	return nil
}

func (p *plainSandbox) Wait() Exit {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit
}

func (p *plainSandbox) Destroy() error {
	if p.runDir == "" {
		return nil
	}
	if err := os.RemoveAll(p.runDir); err != nil {
		return fmt.Errorf("remove guest runtime folder: %w", err)
	}
	return nil
}

func plainGuestEnv(runDir string) []string {
	env := []string{
		brand.Env("GUEST_RUN_DIR") + "=" + runDir,
	}
	for _, k := range []string{"PATH", "HOME", "GORACE", "GOCOVERDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func exitFrom(err error) Exit {
	if err == nil {
		return Exit{}
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return Exit{Code: 1, Err: err}
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return Exit{Code: 128 + int(ws.Signal()), Signal: ws.Signal().String(), Err: err}
	}
	return Exit{Code: ee.ExitCode(), Err: err}
}
