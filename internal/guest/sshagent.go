package guest

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/memberenv"
	"github.com/iamlakshya/simstim/internal/proto"
)

const sshAgentConnections = 16

// SSHAgentProxy carries the guest's Unix agent connections over its existing
// supervisor link. It contains no private key and opens no listening port.
type SSHAgentProxy struct {
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	sess        *link.Session
	listener    *net.UnixListener
	path        string
	active      map[io.Closer]struct{}
	connections int
	closed      bool
	wg          sync.WaitGroup
}

func startSSHAgentProxy(ctx context.Context, sess *link.Session, path string, vm bool) (*SSHAgentProxy, error) {
	if ctx == nil || ctx.Err() != nil || sess == nil || !filepath.IsAbs(path) {
		return nil, execError("the SSH agent has no active guest owner")
	}
	// Never remove a path supplied by another process. The run folder is new
	// at each boot, and an unexpected existing entry makes this start fail.
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, execError("the guest SSH agent socket path is already occupied")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, execError("the guest SSH agent socket cannot be created")
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, execError("the guest SSH agent socket permissions cannot be set")
	}
	if vm {
		if err := os.Chown(path, AgentUID, AgentGID); err != nil {
			_ = listener.Close()
			return nil, execError("the guest SSH agent socket owner cannot be set")
		}
	}
	lifetime, cancel := context.WithCancel(ctx)
	p := &SSHAgentProxy{ctx: lifetime, cancel: cancel, sess: sess, listener: listener, path: path, active: make(map[io.Closer]struct{})}
	p.wg.Add(1)
	go p.accept()
	return p, nil
}

func (p *SSHAgentProxy) accept() {
	defer p.wg.Done()
	for {
		connection, err := p.listener.AcceptUnix()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed || p.ctx.Err() != nil || p.connections >= sshAgentConnections {
			p.mu.Unlock()
			_ = connection.Close()
			continue
		}
		p.active[connection] = struct{}{}
		p.connections++
		p.wg.Add(1)
		p.mu.Unlock()
		go func() {
			defer p.wg.Done()
			defer func() { p.mu.Lock(); p.connections--; p.mu.Unlock() }()
			p.serve(connection)
		}()
	}
}

func (p *SSHAgentProxy) serve(connection *net.UnixConn) {
	defer connection.Close()
	defer p.forget(connection)
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	stream, err := p.sess.Open(ctx, link.KindAgent, struct{}{})
	cancel()
	if err != nil {
		return
	}
	defer stream.Close()
	p.mu.Lock()
	if p.closed || p.ctx.Err() != nil {
		p.mu.Unlock()
		return
	}
	p.active[stream] = struct{}{}
	p.mu.Unlock()
	defer p.forget(stream)
	spliceGuestFlow(stream, connection)
}

func (p *SSHAgentProxy) forget(closer io.Closer) {
	p.mu.Lock()
	delete(p.active, closer)
	p.mu.Unlock()
}

// Close excludes new streams, closes both ends of every active relay and joins
// the handlers before the guest removes its runtime files.
func (p *SSHAgentProxy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.closed = true
	p.cancel()
	_ = p.listener.Close()
	for closer := range p.active {
		if deadline, ok := closer.(interface{ SetDeadline(time.Time) error }); ok {
			_ = deadline.SetDeadline(time.Now())
		}
		_ = closer.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
	return nil
}

func (g *Init) memberSSHAgentEnv(a *proto.CtlStartArgs) error {
	if a.SSHAgent {
		_, err := g.helperExecutable()
		if err != nil {
			return err
		}
		if g.root == "" {
			if err := checkNamedSSHHelperProjectionV1(); err != nil {
				return err
			}
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended {
		return execError("the guest ended before its SSH agent was prepared")
	}
	if a.SSHAgent && g.sshAgent == nil {
		proxy, err := startSSHAgentProxy(g.lifetime, g.sess, g.path(filepath.Join(host.GuestRunDir(), "ssh-agent.sock")), g.vm != nil)
		if err != nil {
			return err
		}
		g.sshAgent = proxy
	}
	if !a.SSHAgent {
		return nil
	}
	a.Env = memberenv.NamedSSHEnvironment(a.Env, a.SSHAgent)
	return nil
}

func (g *Init) stopSSHAgent() {
	g.mu.Lock()
	proxy := g.sshAgent
	g.sshAgent = nil
	g.mu.Unlock()
	_ = proxy.Close()
}
