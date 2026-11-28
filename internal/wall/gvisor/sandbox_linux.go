//go:build linux

package gvisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

// Sandbox runs one session's guest init inside upstream runsc (gVisor Sentry)
// with --network=none, --directfs=false, --ignore-cgroups, --host-uds=none and
// the supervisor's socketpair passed as guest fd 3 via --pass-fd.
type Sandbox struct {
	spec Spec

	mu          sync.Mutex
	started     bool
	runscPath   string
	rootDir     string
	bundleDir   string
	containerID string
	cmd         *exec.Cmd
	exited      chan struct{}
	done        chan struct{}
	exit        wall.Exit
	stderr      lockedBuf
}

// NewSandbox creates an unstarted gVisor wall sandbox for spec.
func NewSandbox(spec Spec) *Sandbox {
	return &Sandbox{
		spec:   spec,
		exited: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start writes the OCI bundle under SessionDir/bundle, starts upstream runsc
// passing the guest end of the internal/link socketpair on fd 3, and returns
// the supervisor end of the socketpair.
func (s *Sandbox) Start(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, errors.New("sandbox already started. Try: stop the session first")
	}

	runscPath, err := s.resolveRunsc()
	if err != nil {
		return nil, err
	}
	sessDir := s.spec.SessionDir
	if sessDir == "" {
		sessDir = paths.SessionDir(s.spec.SessionID)
	}
	rootDir := filepath.Join(sessDir, "runsc")
	bundleDir := filepath.Join(sessDir, "bundle")
	if err := paths.EnsurePrivateDir(rootDir); err != nil {
		return nil, err
	}
	if _, err := PrepareBundle(bundleDir, s.spec); err != nil {
		return nil, err
	}

	cid := containerIDFor(s.spec.SessionID)
	args := RunArgs(rootDir, bundleDir, cid, 3)
	if err := ValidateRunArgs(args); err != nil {
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

	runCtx := context.WithoutCancel(ctx)
	cmd := exec.CommandContext(runCtx, runscPath, args...)
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = &s.stderr
	cmd.ExtraFiles = []*os.File{guestFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	startErr := cmd.Start()
	_ = devnull.Close()
	_ = guestFile.Close()
	if startErr != nil {
		_ = hostFile.Close()
		return nil, fmt.Errorf("start runsc: %w. Try: verify the pinned gVisor runtime and run host doctor", startErr)
	}

	conn, err := net.FileConn(hostFile)
	_ = hostFile.Close()
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("wrap link socket: %w", err)
	}

	s.started = true
	s.runscPath = runscPath
	s.rootDir = rootDir
	s.bundleDir = bundleDir
	s.containerID = cid
	s.cmd = cmd
	go s.reap(runCtx, cmd, runscPath, rootDir, cid)
	return conn, nil
}

func (s *Sandbox) resolveRunsc() (string, error) {
	if s.spec.RunscPath != "" {
		return s.spec.RunscPath, nil
	}
	return FindPinnedRunsc(runtime.GOARCH)
}

func (s *Sandbox) reap(ctx context.Context, cmd *exec.Cmd, runscPath, rootDir, cid string) {
	err := cmd.Wait()
	close(s.exited)
	ex := exitFrom(err)
	// Ensure no stale container directory lingers in rootDir after exit.
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	_ = exec.CommandContext(dctx, runscPath, DeleteArgs(rootDir, cid)...).Run()
	cancel()
	s.mu.Lock()
	s.exit = ex
	s.mu.Unlock()
	close(s.done)
}

// Stderr returns bounded diagnostic stderr captured from runsc.
func (s *Sandbox) Stderr() string { return strings.TrimSpace(s.stderr.String()) }

// Stop asks the container inside runsc to exit cleanly via SIGTERM, falling
// back to Kill after grace.
func (s *Sandbox) Stop(ctx context.Context, grace time.Duration) error {
	select {
	case <-s.done:
		return nil
	default:
	}
	s.mu.Lock()
	cmd := s.cmd
	runscPath := s.runscPath
	rootDir := s.rootDir
	cid := s.containerID
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-s.exited:
		<-s.done
		return nil
	default:
	}
	kctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := exec.CommandContext(kctx, runscPath, KillArgs(rootDir, cid, "TERM")...).Run()
	cancel()
	if err != nil {
		select {
		case <-s.exited:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	if grace <= 0 {
		grace = 5 * time.Second
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-s.done:
		return nil
	case <-timer.C:
		return s.killWithContext(context.WithoutCancel(ctx))
	case <-ctx.Done():
		return s.killWithContext(context.WithoutCancel(ctx))
	}
}

// Kill forcefully stops the gVisor sandbox and waits for runsc to exit.
func (s *Sandbox) Kill() error {
	return s.killWithContext(context.Background())
}

func (s *Sandbox) killWithContext(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	default:
	}
	s.mu.Lock()
	cmd := s.cmd
	runscPath := s.runscPath
	rootDir := s.rootDir
	cid := s.containerID
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-s.exited:
		<-s.done
		return nil
	default:
	}
	kctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_ = exec.CommandContext(kctx, runscPath, KillArgs(rootDir, cid, "KILL")...).Run()
	cancel()
	select {
	case <-s.exited:
	default:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	<-s.done
	return nil
}

// Wait blocks until runsc exits and returns its exit status.
func (s *Sandbox) Wait() wall.Exit {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

// Abilities reports optional sandbox abilities for the gVisor wall.
func (*Sandbox) Abilities() wall.Abilities { return wall.Abilities{} }

// Destroy cleans up the transient runsc root and OCI bundle directories in the
// session folder while keeping /work and kept StateDirs intact across stops.
func (s *Sandbox) Destroy() error {
	s.mu.Lock()
	rootDir := s.rootDir
	bundleDir := s.bundleDir
	s.mu.Unlock()
	if rootDir == "" && s.spec.SessionDir != "" {
		rootDir = filepath.Join(s.spec.SessionDir, "runsc")
	}
	if bundleDir == "" && s.spec.SessionDir != "" {
		bundleDir = filepath.Join(s.spec.SessionDir, "bundle")
	}
	var firstErr error
	for _, dir := range []string{rootDir, bundleDir} {
		if dir == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func containerIDFor(sessionID string) string {
	var sb strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	if sb.Len() == 0 {
		return "session"
	}
	return "s-" + sb.String()
}

func exitFrom(err error) wall.Exit {
	if err == nil {
		return wall.Exit{}
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return wall.Exit{Code: 1, Err: err}
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return wall.Exit{Code: 128 + int(ws.Signal()), Signal: ws.Signal().String(), Err: err}
	}
	return wall.Exit{Code: ee.ExitCode(), Err: err}
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() < 8192 {
		rem := 8192 - l.b.Len()
		if len(p) > rem {
			l.b.Write(p[:rem])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
