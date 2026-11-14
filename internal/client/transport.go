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
		if errors.As(err, &ee) && ee.Exited() && p.unreachable != nil && p.unreachable(ee.ExitCode(), p.Stderr()) {
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

// Start runs remote on the host over ssh -T, for the bridge. Through Command,
// it forwards nothing: no agent, no X11 display and no port. Its destination
// ssh writes its own diagnostics to a private -E log file, kept apart from
// shared stderr.
func (s *SSH) Start(ctx context.Context, remote string) (Proc, error) {
	return s.startWith(ctx, ctx, remote)
}

func (s *SSH) startWith(lockCtx, procCtx context.Context, remote string) (Proc, error) {
	lockCtx, release, err := s.beginSSHStart(lockCtx, "connect")
	if err != nil {
		return nil, err
	}
	defer release()
	var logFile *sshLogFile
	if s.bindingCollector != nil {
		logFile = &sshLogFile{path: s.bindingCollector.log.Path(), agent: s.bindingCollector.log}
	} else {
		logFile, err = newSSHLog()
		if err != nil {
			return nil, err
		}
	}
	cmd, err := s.command(lockCtx, procCtx, remote, "-T", "-E", logFile.path)
	if err != nil {
		logFile.finish()
		return nil, err
	}
	var nativeProgram *nativeAgentProgram
	if s.bindingCollector != nil {
		nativeProgram, err = openNativeAgentProgram(cmd.Path)
		if err != nil {
			logFile.finish()
			return nil, err
		}
		defer nativeProgram.Close()
	}
	// ssh exits with 255 when it could not reach the host.
	p, err := CommandProc(cmd, func(code int, _ string) bool { return code == 255 })
	release()
	if err != nil {
		logFile.finish()
		return nil, err
	}
	wrapped := &sshProc{Proc: p, rawDone: make(chan struct{}), ctx: context.WithoutCancel(lockCtx), host: s.Host, args: cmd.Args, logFile: logFile}
	if s.bindingCollector != nil {
		if err := s.bindingCollector.attachOwner(wrapped, procCtx, nativeProgram); err != nil {
			wrapped.Kill()
			_ = wrapped.Wait()
			return nil, err
		}
	}
	return wrapped, nil
}

type sshProc struct {
	ctx context.Context
	Proc
	host    Host
	args    []string
	logFile *sshLogFile

	once    sync.Once
	rawDone chan struct{}
	waitErr error
	code    int

	logMu            sync.Mutex
	log              string
	nativeDiag       nativeSSHDiagnosis
	nativeClassified bool
}

func (p *sshProc) Wait() error {
	p.once.Do(func() {
		p.waitErr = p.Proc.Wait()
		if p.rawDone != nil {
			close(p.rawDone)
		}
		log := ""
		var diagnosis nativeSSHDiagnosis
		classified := false
		if p.logFile != nil && p.logFile.agent != nil {
			summary, err := p.logFile.agent.FinishScan(func(reader io.Reader) error {
				var scanError error
				diagnosis, scanError = scanNativeSSHDiagnosis(reader, pinAlias(p.host.Name))
				return scanError
			})
			p.logFile.agentMu.Lock()
			p.logFile.agentErr = err
			p.logFile.agentMu.Unlock()
			log, classified = summary.Text, err == nil && summary.Complete && summary.Classified
		} else {
			log = p.logFile.finish()
		}
		p.logMu.Lock()
		p.log, p.code, p.nativeClassified = log, exitCode(p.waitErr), classified
		// A timed-out callback may still unwind. Read its result only
		// after FinishScan has confirmed successful complete classification.
		if classified {
			p.nativeDiag = diagnosis
		}
		p.logMu.Unlock()
		if p.host.SSH != "" && ValidHostName(p.host.Name) && !pinned(p.host.Name) {
			cleanupUnpinnedMaster(p.ctx, p.host, p.args)
		}
	})
	return p.waitErr
}

func (p *sshProc) Stderr() string {
	if s := p.Proc.Stderr(); s != "" {
		return s
	}
	p.logMu.Lock()
	defer p.logMu.Unlock()
	return p.log
}

func (p *sshProc) sshDiag() (string, int) {
	_ = p.Wait()
	return p.log, p.code
}

// ReachHint is the ssh command that should work before anything else does.
func (s *SSH) ReachHint() string {
	return SSHCommand(s.Host.SSH) + " (check that this works first)"
}

func (p *sshProc) agentFailure() error {
	if p.logFile != nil && p.logFile.agent != nil && p.rawDone != nil {
		select {
		case <-p.rawDone:
			if failure := agentProcessExitFailure(exitCode(p.waitErr)); failure != nil {
				return failure
			}
		default:
		}
	}
	if p.logFile != nil && p.logFile.agent != nil {
		if err := p.logFile.agent.Failure(); err != nil {
			return agentNativeResourceError(err)
		}
		p.logFile.agentMu.Lock()
		err := p.logFile.agentErr
		p.logFile.agentMu.Unlock()
		if err != nil {
			return agentNativeResourceError(err)
		}
	}
	return nil
}
