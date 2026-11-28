//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

// Sandbox runs one session's guest init inside bubblewrap with new user, mount,
// PID, IPC, UTS, cgroup and (unless ShareNet) network namespaces, plus seccomp
// and Landlock confinement.
type Sandbox struct {
	spec Spec

	mu      sync.Mutex
	started bool
	cmd     *exec.Cmd
	done    chan struct{}
	exit    wall.Exit
	stderr  lockedBuf
}

// NewSandbox creates an unstarted bubblewrap process-wall sandbox for spec.
func NewSandbox(spec Spec) *Sandbox {
	return &Sandbox{
		spec: spec,
		done: make(chan struct{}),
	}
}

// Start launches bubblewrap and returns the supervisor end of the internal/link
// Unix socketpair. The guest init inherits the other end on fd 3.
func (s *Sandbox) Start(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, errors.New("sandbox already started. Try: stop the session first")
	}

	spec := s.spec
	if !spec.DisableUserNS {
		spec.DisableUserNS = supportsDisableUserNS(ctx, spec.BwrapPath)
	}
	plan, err := BuildPlan(spec)
	if err != nil {
		return nil, err
	}
	for _, dir := range plan.StatePaths {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return nil, fmt.Errorf("prepare session state folder %s: %w", dir, err)
		}
	}
	for _, dir := range plan.CachePaths {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return nil, fmt.Errorf("prepare session cache folder %s: %w", dir, err)
		}
	}

	seccompBytes, err := SeccompBPF()
	if err != nil {
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

	extraFiles := make([]*os.File, 0, 2+len(plan.BindData))
	extraFiles = append(extraFiles, guestFile)
	closeExtras := func() {
		for _, f := range extraFiles {
			_ = f.Close()
		}
	}

	secFile, err := memFile("seccomp-bpf", seccompBytes)
	if err != nil {
		_ = hostFile.Close()
		closeExtras()
		return nil, fmt.Errorf("stage seccomp filter: %w", err)
	}
	extraFiles = append(extraFiles, secFile)

	for _, bd := range plan.BindData {
		f, ferr := memFile(bd.Name, bd.Data)
		if ferr != nil {
			_ = hostFile.Close()
			closeExtras()
			return nil, fmt.Errorf("stage %s: %w", bd.Dest, ferr)
		}
		extraFiles = append(extraFiles, f)
	}

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = hostFile.Close()
		closeExtras()
		return nil, fmt.Errorf("open /dev/null: %w", err)
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), plan.BwrapPath, plan.Args...)
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = &s.stderr
	cmd.ExtraFiles = extraFiles
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	startErr := cmd.Start()
	_ = devnull.Close()
	closeExtras()
	if startErr != nil {
		_ = hostFile.Close()
		return nil, fmt.Errorf("start bubblewrap: %w. Try: install bubblewrap and run host doctor", startErr)
	}

	conn, err := net.FileConn(hostFile)
	_ = hostFile.Close()
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("wrap link socket: %w", err)
	}

	s.started = true
	s.cmd = cmd
	go s.reap(cmd)
	return conn, nil
}

func (s *Sandbox) reap(cmd *exec.Cmd) {
	err := cmd.Wait()
	ex := exitFrom(err)
	s.mu.Lock()
	s.exit = ex
	s.mu.Unlock()
	close(s.done)
}

// Stderr returns the bounded diagnostic stderr captured from bubblewrap.
func (s *Sandbox) Stderr() string { return strings.TrimSpace(s.stderr.String()) }

// Stop asks bubblewrap to exit gracefully, falling back to SIGKILL after grace.
func (s *Sandbox) Stop(ctx context.Context, grace time.Duration) error {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
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
	case <-s.done:
		return nil
	case <-timer.C:
		return s.Kill()
	case <-ctx.Done():
		return s.Kill()
	}
}

// Kill sends SIGKILL to the bubblewrap process group and waits for exit.
func (s *Sandbox) Kill() error {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-s.done
	return nil
}

// Wait blocks until bubblewrap exits and returns its exit status.
func (s *Sandbox) Wait() wall.Exit {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

// Abilities reports optional sandbox abilities for the process wall.
func (*Sandbox) Abilities() wall.Abilities { return wall.Abilities{} }

// Destroy releases any temporary sandbox resources after Wait returns.
// Session workspace and kept StateDirs belong to the session folder and stay
// across stops until session deletion.
func (s *Sandbox) Destroy() error { return nil }

// BwrapVersion returns the installed bubblewrap version string (for example
// "0.12.0"), or "" if bwrap is missing or could not be queried.
func BwrapVersion(ctx context.Context, bwrapPath string) string {
	if bwrapPath == "" {
		var err error
		bwrapPath, err = exec.LookPath("bwrap")
		if err != nil {
			return ""
		}
	}
	vctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, bwrapPath, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 2 && strings.EqualFold(fields[0], "bubblewrap") {
		return fields[1]
	}
	return strings.TrimSpace(string(out))
}

// supportsDisableUserNS reports whether bwrap supports --disable-userns (>= 0.8.0).
func supportsDisableUserNS(ctx context.Context, bwrapPath string) bool {
	ver := BwrapVersion(ctx, bwrapPath)
	parts := strings.Split(ver, ".")
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	minVal, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return maj > 0 || minVal >= 8
}

// memFile creates an anonymous readable file holding data, backed by memfd on
// Linux (falling back to an unlinked temp file if memfd_create is unavailable).
func memFile(name string, data []byte) (*os.File, error) {
	if fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC); err == nil {
		f := os.NewFile(uintptr(fd), name)
		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("write memfd %s: %w", name, werr)
		}
		if _, serr := f.Seek(0, 0); serr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("seek memfd %s: %w", name, serr)
		}
		return f, nil
	}
	f, err := os.CreateTemp("", "wall-"+name+"-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file %s: %w", name, err)
	}
	_ = os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write temp file %s: %w", name, err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("seek temp file %s: %w", name, err)
	}
	return f, nil
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

// lockedBuf keeps up to 8 KiB of diagnostic stderr safely across goroutines.
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
