//go:build unix

package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var defaultDockerdPaths = []string{
	"/usr/bin/dockerd",
	"/usr/local/bin/dockerd",
	"/usr/sbin/dockerd",
	"/sbin/dockerd",
}

const (
	defaultDockerPublicSock = "/run/docker.sock"
	defaultDockerDaemonSock = "/run/dockerd.sock"
	defaultDockerDataRoot   = "/var/lib/docker"
	defaultDockerExecRoot   = "/run/docker"
	defaultDockerPIDFile    = "/run/docker.pid"
)

// findDockerd locates dockerd in the guest rootfs (spec 7.5, question 98).
func findDockerd(candidates ...string) string {
	if len(candidates) == 0 {
		candidates = defaultDockerdPaths
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// dockerOptions configures a lazy-starting dockerd socket activator.
type dockerOptions struct {
	DockerdPath string
	PublicSock  string
	DaemonSock  string
	DataRoot    string
	ExecRoot    string
	PIDFile     string
	DockerGID   int
	ProxyURL    string
	StartWait   time.Duration
}

// dockerManager listens on /run/docker.sock (0660 root:docker) and starts the
// image's dockerd lazily on first client connection (isolation spec 7.5).
type dockerManager struct {
	opts dockerOptions
	ln   net.Listener

	mu      sync.Mutex
	cmd     *exec.Cmd
	done    chan struct{}
	stopped bool

	wg sync.WaitGroup
}

func startDockerActivator(ctx context.Context, opts dockerOptions) (*dockerManager, error) {
	if opts.DockerdPath == "" {
		opts.DockerdPath = findDockerd()
	}
	if opts.DockerdPath == "" {
		return nil, errors.New("dockerd not found in image. Try: pass --image with an image that includes Docker")
	}
	if opts.PublicSock == "" {
		opts.PublicSock = defaultDockerPublicSock
	}
	if opts.DaemonSock == "" {
		opts.DaemonSock = defaultDockerDaemonSock
	}
	if opts.DataRoot == "" {
		opts.DataRoot = defaultDockerDataRoot
	}
	if opts.ExecRoot == "" {
		opts.ExecRoot = defaultDockerExecRoot
	}
	if opts.PIDFile == "" {
		opts.PIDFile = defaultDockerPIDFile
	}
	if opts.StartWait <= 0 {
		opts.StartWait = 15 * time.Second
	}

	//nolint:gosec // G301: /run and docker socket dir must allow group traversal
	if err := os.MkdirAll(filepath.Dir(opts.PublicSock), 0o755); err != nil {
		return nil, fmt.Errorf("create docker socket dir: %w", err)
	}
	_ = os.Remove(opts.PublicSock)

	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "unix", opts.PublicSock)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", opts.PublicSock, err)
	}
	if opts.DockerGID > 0 && os.Geteuid() == 0 {
		_ = os.Chown(opts.PublicSock, 0, opts.DockerGID)
	}
	//nolint:gosec // G302: /run/docker.sock must be 0660 so docker group members can connect
	if err := os.Chmod(opts.PublicSock, 0o660); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod %s: %w", opts.PublicSock, err)
	}

	// Ensure /var/run/docker.sock resolves when /var/run is a real directory
	// instead of a symlink to /run.
	if opts.PublicSock == defaultDockerPublicSock {
		ensureVarRunDockerSock(opts.PublicSock)
	}

	m := &dockerManager{opts: opts, ln: ln}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.acceptLoop(ctx)
	}()
	return m, nil
}

func ensureVarRunDockerSock(target string) {
	if fi, err := os.Lstat("/var/run"); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return
	}
	//nolint:gosec // G301: /var/run must be world-traversable
	_ = os.MkdirAll("/var/run", 0o755)
	_ = os.Remove("/var/run/docker.sock")
	_ = os.Symlink(target, "/var/run/docker.sock")
}

func (m *dockerManager) acceptLoop(ctx context.Context) {
	for {
		conn, err := m.ln.Accept()
		if err != nil {
			return
		}
		m.wg.Add(1)
		go func(c net.Conn) {
			defer m.wg.Done()
			m.handleConn(ctx, c)
		}(conn)
	}
}

func (m *dockerManager) handleConn(ctx context.Context, client net.Conn) {
	defer func() { _ = client.Close() }()
	if err := m.ensureRunning(ctx); err != nil {
		return
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	backend, err := d.DialContext(ctx, "unix", m.opts.DaemonSock)
	if err != nil {
		return
	}
	defer func() { _ = backend.Close() }()

	stop := context.AfterFunc(ctx, func() {
		_ = client.Close()
		_ = backend.Close()
	})
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(backend, client)
		if cw, ok := backend.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, backend)
		if cw, ok := client.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
	}()
	wg.Wait()
}

func (m *dockerManager) ensureRunning(ctx context.Context) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return errors.New("docker manager stopped")
	}
	if m.cmd != nil {
		select {
		case <-m.done:
			m.cmd = nil
			m.done = nil
		default:
			done := m.done
			m.mu.Unlock()
			return waitForUnixSocket(ctx, m.opts.DaemonSock, m.opts.StartWait, done)
		}
	}

	_ = os.MkdirAll(m.opts.DataRoot, 0o700)
	_ = os.MkdirAll(m.opts.ExecRoot, 0o700)
	_ = os.Remove(m.opts.DaemonSock)
	_ = os.Remove(m.opts.PIDFile)

	args := []string{
		"--host=unix://" + m.opts.DaemonSock,
		"--data-root=" + m.opts.DataRoot,
		"--exec-root=" + m.opts.ExecRoot,
		"--pidfile=" + m.opts.PIDFile,
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), m.opts.DockerdPath, args...)
	cmd.Env = dockerdEnv(m.opts.ProxyURL)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin = devnull
		cmd.Stdout = devnull
		cmd.Stderr = devnull
		defer devnull.Close()
	}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("start dockerd: %w", err)
	}
	done := make(chan struct{})
	m.cmd = cmd
	m.done = done
	m.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return waitForUnixSocket(ctx, m.opts.DaemonSock, m.opts.StartWait, done)
}

func dockerdEnv(proxyURL string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
	if proxyURL != "" {
		env = append(env,
			"HTTP_PROXY="+proxyURL,
			"HTTPS_PROXY="+proxyURL,
			"http_proxy="+proxyURL,
			"https_proxy="+proxyURL,
			"NO_PROXY=localhost,127.0.0.1,::1",
			"no_proxy=localhost,127.0.0.1,::1",
		)
	}
	return env
}

func waitForUnixSocket(ctx context.Context, sock string, timeout time.Duration, exited <-chan struct{}) error {
	deadline := time.Now().Add(timeout)
	for {
		d := net.Dialer{Timeout: 200 * time.Millisecond}
		c, err := d.DialContext(ctx, "unix", sock)
		if err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-exited:
			return errors.New("dockerd exited before listening")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dockerd did not listen on %s within %s", sock, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Stop closes the docker activator socket and terminates dockerd cleanly if it
// was started.
func (m *dockerManager) Stop(grace time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.stopped = true
	cmd := m.cmd
	done := m.done
	m.mu.Unlock()

	if m.ln != nil {
		_ = m.ln.Close()
	}
	_ = os.Remove(m.opts.PublicSock)

	if cmd != nil && cmd.Process != nil && done != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if grace <= 0 {
			grace = 3 * time.Second
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	m.wg.Wait()
}
