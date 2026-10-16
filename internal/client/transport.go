package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Transport runs commands on a host and carries their standard input and
// output. The helper's protocol needs nothing more: a two-way, binary-safe
// byte stream to one process. SSH gives one, and so do `docker exec -i` and
// E2B's process API (see docs/contributing/decisions/0013-host-providers.md).
type Transport interface {
	// Start runs remote, a POSIX shell command line, on the host. Its stdin
	// and stdout stay open until the command ends or Kill is called. When ctx
	// ends, the command is killed.
	Start(ctx context.Context, remote string) (Proc, error)
	// Run runs remote to its end, with stdin as its input, and returns what
	// it wrote to stdout. A failure carries what it wrote to stderr.
	Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error)
	// ReachHint is what the user can run to check that the host can be
	// reached at all.
	ReachHint() string
}

// Proc is one command that a Transport started.
type Proc interface {
	// Stdin is the command's standard input. Closing it sends EOF.
	Stdin() io.WriteCloser
	// Stdout is the command's standard output, byte for byte.
	Stdout() io.Reader
	// Wait waits for the command to end. Its error wraps ErrUnreachable when
	// the host could not be reached at all. It may be called more than once.
	Wait() error
	// Kill ends the command at once.
	Kill()
	// Stderr is the start of what the command wrote to stderr.
	Stderr() string
}

// KeepAliver is a Transport whose commands do not end when the client goes
// away. The client then pings the bridge every KeepAlive, and the bridge
// exits when the pings stop.
type KeepAliver interface {
	KeepAlive() time.Duration
}

// ErrUnreachable marks a command that failed because the host could not be
// reached, as opposed to a command that ran and failed.
var ErrUnreachable = errors.New("the host could not be reached")

// CommandProc starts cmd, a local program that carries a remote command, and
// returns it as a Proc. unreachable decides from the exit code and stderr
// whether a failure means the host could not be reached.
func CommandProc(cmd *exec.Cmd, unreachable func(code int, stderr string) bool) (Proc, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open the command's stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open the command's stdout: %w", err)
	}
	p := &cmdProc{cmd: cmd, stdin: stdin, stdout: stdout, unreachable: unreachable}
	cmd.Stderr = &p.stderr
	if w := stderrLines(); w != nil {
		// With the net category on, the debug file gets all of it.
		cmd.Stderr = io.MultiWriter(&p.stderr, w)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	return p, nil
}

type cmdProc struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.Reader
	stderr      lockedWriter
	unreachable func(code int, stderr string) bool

	once    sync.Once
	waitErr error
}

func (p *cmdProc) Stdin() io.WriteCloser { return p.stdin }
func (p *cmdProc) Stdout() io.Reader     { return p.stdout }
func (p *cmdProc) Stderr() string        { return p.stderr.String() }

func (p *cmdProc) Wait() error {
	p.once.Do(func() {
		err := p.cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) && p.unreachable != nil && p.unreachable(ee.ExitCode(), p.Stderr()) {
			err = fmt.Errorf("%w: %w", ErrUnreachable, err)
		}
		p.waitErr = err
	})
	return p.waitErr
}

func (p *cmdProc) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// lockedWriter keeps the first 8 KiB written to it, from several goroutines.
type lockedWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() < 8192 {
		l.b.Write(p)
	}
	return len(p), nil
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Start runs remote on the host over ssh -T, for the bridge.
func (s *SSH) Start(ctx context.Context, remote string) (Proc, error) {
	cmd, err := s.Command(ctx, remote, "-T")
	if err != nil {
		return nil, err
	}
	// ssh exits with 255 when it could not reach the host.
	return CommandProc(cmd, func(code int, _ string) bool { return code == 255 })
}

// ReachHint is the ssh command that should work before anything else does.
func (s *SSH) ReachHint() string {
	return fmt.Sprintf("ssh %s   (check that this works first)", s.Host.SSH)
}
