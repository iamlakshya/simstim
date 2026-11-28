package chv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

const (
	// maxVMMStderrBytes caps retained VMM stderr per spec 20.3 (64 KiB).
	maxVMMStderrBytes = 64 << 10
	defaultStopGrace  = 5 * time.Second
)

func init() {
	wall.Register(Driver{})
}

// Driver implements wall.Driver for Cloud Hypervisor microvms (level microvm).
type Driver struct{}

// Name returns "chv".
func (Driver) Name() string { return DriverName }

// Level returns wall.MicroVM.
func (Driver) Level() wall.Level { return wall.MicroVM }

// Probe checks whether this host can boot Cloud Hypervisor microvms.
func (Driver) Probe(ctx context.Context) wall.Probe {
	return ProbeWith(ctx, defaultProbeDeps())
}

// Prepare creates a Cloud Hypervisor sandbox handle from spec using the default
// pinned runtime store and session paths.
func (Driver) Prepare(_ context.Context, spec wall.Spec) (wall.Sandbox, error) {
	if spec.ID == "" {
		return nil, errors.New("chv prepare: missing session ID. Try: pass a valid wall.Spec")
	}
	rp := DefaultRuntimePaths()
	cpus := spec.CPUs
	if cpus <= 0 {
		cpus = 2
	}
	mem := spec.MemoryMax
	if mem <= 0 {
		mem = 1 << 30 // 1 GiB fallback if caller left MemoryMax unset
	}
	diskMax := spec.DiskMax
	if diskMax <= 0 {
		diskMax = DefaultSessionDiskBytes
	}
	cfg := VMConfig{
		VMMPath:         rp.VMM,
		CHRemotePath:    rp.CHRemote,
		APISocket:       paths.VMMSocket(spec.ID),
		VsockSocket:     paths.VsockSocket(spec.ID),
		KernelPath:      rp.Kernel,
		CPUs:            cpus,
		MemoryBytes:     mem,
		SessionDiskPath: paths.SessionDisk(spec.ID),
		SessionDirect:   true,
	}
	if spec.Image != nil && spec.Image.Path != "" {
		cfg.Image = &DiskVerity{
			Path:          spec.Image.Path,
			RootHash:      spec.Image.RootHash,
			HasSuperblock: true,
		}
	}
	return NewSandbox(SandboxOptions{
		Config:           cfg,
		SessionDiskBytes: diskMax,
	}), nil
}

// Open attaches to a running Cloud Hypervisor microvm by session ID via its
// VMM API socket (P3.6).
func (Driver) Open(ctx context.Context, id string) (wall.Sandbox, error) {
	return openSandbox(ctx, id)
}

// SandboxOptions configures NewSandbox.
type SandboxOptions struct {
	Config           VMConfig
	SessionDiskBytes int64
	MkfsExt4Path     string
	BwrapPath        string // optional bubblewrap path override (empty auto-detects or falls back via internal/jail)
	DisableJail      bool   // disables internal/jail confinement (for unit tests with fake VMM scripts only)
	BootTimeout      time.Duration
	VsockInterval    time.Duration
	VsockPort        uint32
	SkipVsock        bool // true for --self-test serial-only boots (spec 20.1)
}

// jailCommand builds a jailed *exec.Cmd for the VMM. Tests replace it.
var jailCommand = jail.Command

// Sandbox manages one Cloud Hypervisor microvm process and its sockets.
type Sandbox struct {
	opts SandboxOptions

	mu         sync.Mutex
	started    bool
	cmd        *exec.Cmd
	stderr     *tailBuffer
	vsockReply string
	exited     chan struct{}
	done       chan struct{}
	exit       wall.Exit
}

// NewSandbox builds a Cloud Hypervisor wall.Sandbox from opts.
func NewSandbox(opts SandboxOptions) *Sandbox {
	if opts.BootTimeout <= 0 {
		opts.BootTimeout = DefaultBootTimeout
	}
	if opts.VsockInterval <= 0 {
		opts.VsockInterval = DefaultVsockRetryInterval
	}
	if opts.VsockPort == 0 {
		opts.VsockPort = DefaultVsockPort
	}
	return &Sandbox{
		opts:   opts,
		exited: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start creates the session disk if absent, launches Cloud Hypervisor, and
// connects to guest init over virtio-vsock (unless SkipVsock is set for a
// serial self-test).
func (s *Sandbox) Start(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("chv start cancelled: %w", err)
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil, errors.New("microvm sandbox already started. Try: stop the session first")
	}
	s.started = true
	s.mu.Unlock()

	cfg := s.opts.Config
	if cfg.VMMPath == "" {
		return nil, errors.New("chv start: VMMPath is empty. Try: install Cloud Hypervisor v" + PinnedVMMVersion)
	}
	if cfg.SessionDiskPath != "" {
		if _, err := EnsureSessionDisk(ctx, cfg.SessionDiskPath, s.opts.SessionDiskBytes, s.opts.MkfsExt4Path); err != nil {
			return nil, err
		}
	}
	for _, sock := range []string{cfg.APISocket, cfg.VsockSocket} {
		if err := paths.EnsurePrivateDir(filepath.Dir(sock)); err != nil {
			return nil, fmt.Errorf("prepare VMM socket directory: %w", err)
		}
		_ = os.Remove(sock)
	}

	vmmArgv, err := BuildArgv(cfg)
	if err != nil {
		return nil, err
	}

	stderr := newTailBuffer(maxVMMStderrBytes)
	var cmd *exec.Cmd
	jailed := !s.opts.DisableJail
	if jailed {
		js := VMMJailSpec(cfg, cfg.VMMPath, vmmArgv)
		js.BwrapPath = s.opts.BwrapPath
		cmd, err = jailCommand(context.WithoutCancel(ctx), js)
		if err != nil {
			return nil, fmt.Errorf("sandbox_boot: prepare VMM jail: %w", err)
		}
	} else {
		cmd = exec.CommandContext(context.WithoutCancel(ctx), cfg.VMMPath, vmmArgv...)
	}
	cmd.Stderr = stderr
	if cfg.SerialConsole {
		cmd.Stdout = stderr
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	startErr := cmd.Start()
	if jailed {
		jail.CloseInternalFiles(cmd, 0)
	}
	if startErr != nil {
		return nil, fmt.Errorf("sandbox_boot: start Cloud Hypervisor: %w. Try: run host doctor to check /dev/kvm and the VMM binary", startErr)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.stderr = stderr
	s.mu.Unlock()
	go s.reap(cmd)

	if s.opts.SkipVsock {
		return nil, nil
	}

	bootCtx, cancel := context.WithTimeout(ctx, s.opts.BootTimeout)
	defer cancel()
	conn, reply, err := DialVsockRetry(bootCtx, cfg.VsockSocket, s.opts.VsockPort, s.opts.VsockInterval, s.done)
	if err != nil {
		select {
		case <-s.done:
		default:
			_ = s.Kill()
		}
		if line := lastNonEmptyLine(stderr.String()); line != "" {
			return nil, fmt.Errorf("sandbox_boot: the VMM exited or failed to boot (%s): %w. Try: run host doctor", line, err)
		}
		return nil, fmt.Errorf("sandbox_boot: %w. Try: run host doctor", err)
	}
	s.mu.Lock()
	s.vsockReply = reply
	s.mu.Unlock()
	return conn, nil
}

func (s *Sandbox) reap(cmd *exec.Cmd) {
	err := cmd.Wait()
	close(s.exited)
	ex := exitFrom(err)
	s.mu.Lock()
	s.exit = ex
	s.mu.Unlock()
	close(s.done)
}

// Stop sends an ACPI power-button event via ch-remote, waits up to grace for
// the VMM to exit cleanly, and falls back to Kill if it does not.
func (s *Sandbox) Stop(ctx context.Context, grace time.Duration) error {
	select {
	case <-s.done:
		return nil
	default:
	}
	s.mu.Lock()
	cmd := s.cmd
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

	if grace <= 0 {
		grace = defaultStopGrace
	}
	if s.opts.Config.CHRemotePath != "" && s.opts.Config.APISocket != "" {
		_ = PowerButton(ctx, s.opts.Config.CHRemotePath, s.opts.Config.APISocket)
	} else {
		select {
		case <-s.exited:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
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

// Kill terminates the VMM process group immediately and waits for exit.
func (s *Sandbox) Kill() error {
	select {
	case <-s.done:
		return nil
	default:
	}
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-s.exited:
	default:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	<-s.done
	return nil
}

// Wait blocks until the VMM exits and returns its exit status.
func (s *Sandbox) Wait() wall.Exit {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

// Destroy removes the VMM API socket and vsock Unix socket after exit.
// It leaves the session disk intact for resume or explicit session removal.
func (s *Sandbox) Destroy() error {
	var firstErr error
	for _, p := range []string{s.opts.Config.APISocket, s.opts.Config.VsockSocket} {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return firstErr
}

// Abilities reports optional capabilities of a Cloud Hypervisor sandbox.
func (s *Sandbox) Abilities() wall.Abilities { return wall.Abilities{} }

// Stderr returns the capped (64 KiB) VMM stderr tail captured during the run.
func (s *Sandbox) Stderr() string {
	s.mu.Lock()
	tb := s.stderr
	s.mu.Unlock()
	if tb == nil {
		return ""
	}
	return tb.String()
}

// VsockReply returns the handshake reply line received from Cloud Hypervisor
// on CONNECT (for example "OK <host_port>").
func (s *Sandbox) VsockReply() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vsockReply
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

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// tailBuffer keeps the last cap bytes written to it (thread-safe).
type tailBuffer struct {
	mu  sync.Mutex
	cap int
	buf []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{cap: limit}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if n >= t.cap {
		t.buf = append(t.buf[:0], p[n-t.cap:]...)
		return n, nil
	}
	if len(t.buf)+n > t.cap {
		drop := len(t.buf) + n - t.cap
		copy(t.buf, t.buf[drop:])
		t.buf = t.buf[:len(t.buf)-drop]
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
