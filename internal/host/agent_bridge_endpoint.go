package host

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

type hostAgentEndpoint struct {
	listener  net.Listener
	root      *os.Root
	path      string
	name      string
	witness   string
	info      os.FileInfo
	bound     bool
	claim     *os.File
	claimName string
	claimInfo os.FileInfo
	lease     *agentLease
	stop      sync.Once
	cleanup   sync.Once
	operation sync.Mutex
	err       error
}

func (e *hostAgentEndpoint) publish(ctx context.Context, id string) error {
	e.operation.Lock()
	defer e.operation.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := e.root.Lstat(e.name)
	retained, rerr := e.root.Lstat(e.witness)
	if err != nil || rerr != nil || !privateAgentSocket(current) || !os.SameFile(current, e.info) || !os.SameFile(current, retained) {
		return agentSocketError()
	}
	lease, err := publishAgentEndpointLinkAt(ctx, e.root, id, e.path, strings.TrimPrefix(e.claimName, ".agent-claim-"))
	e.lease = lease
	if err != nil {
		return err
	}
	return nil
}

func (e *hostAgentEndpoint) revoke() {
	if e != nil {
		e.stop.Do(func() {
			if e.listener != nil {
				_ = e.listener.Close()
			}
		})
	}
}

// Close removes only this endpoint's lease, socket, witness and open claim.
func (e *hostAgentEndpoint) Close() error { return e.CloseContext(context.Background()) }

// CloseContext preserves the durable witness/claim if owned socket or
// alias cleanup fails. Closing the held claim releases its kernel lock;
// a later recovery must verify that proof, never adopt a foreign path.
func (e *hostAgentEndpoint) CloseContext(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.cleanup.Do(func() {
		e.revoke()
		e.operation.Lock()
		defer e.operation.Unlock()
		defer e.root.Close()
		if e.claim != nil {
			defer e.claim.Close()
		}
		if err := ctx.Err(); err != nil {
			e.err = err
			return
		}
		if err := e.lease.CloseContext(ctx); err != nil {
			e.err = err
			return
		}
		if e.name != "" && e.listener != nil {
			current, err := e.root.Lstat(e.name)
			switch {
			case errors.Is(err, os.ErrNotExist):
			case err != nil:
				e.err = err
				return
			case e.info == nil || e.info.Mode()&os.ModeSocket == 0 || !os.SameFile(current, e.info):
				e.err = agentSocketError()
				return
			default:
				if err := e.root.Remove(e.name); err != nil && !errors.Is(err, os.ErrNotExist) {
					e.err = err
					return
				}
			}
		}
		// The original socket is now confirmed absent. Only then remove its
		// exact ownership witness, keeping all proof on any failure.
		if err := ctx.Err(); err != nil {
			e.err = err
			return
		}
		if e.witness != "" {
			witness, err := e.root.Lstat(e.witness)
			switch {
			case errors.Is(err, os.ErrNotExist):
			case err != nil:
				e.err = err
				return
			case e.info == nil || !os.SameFile(witness, e.info):
				e.err = agentSocketError()
				return
			default:
				if err := e.root.Remove(e.witness); err != nil && !errors.Is(err, os.ErrNotExist) {
					e.err = err
					return
				}
			}
		}
		if err := ctx.Err(); err != nil {
			e.err = err
			return
		}
		if e.claim != nil {
			current, err := e.root.Lstat(e.claimName)
			switch {
			case errors.Is(err, os.ErrNotExist):
			case err != nil:
				e.err = err
				return
			case e.claimInfo == nil || !os.SameFile(current, e.claimInfo):
				e.err = agentSocketError()
				return
			default:
				if err := e.root.Remove(e.claimName); err != nil && !errors.Is(err, os.ErrNotExist) {
					e.err = err
					return
				}
			}
		}
		e.err = ctx.Err()
	})
	return e.err
}

func (b *hostAgentBridge) bind() error {
	if err := checkAgentBridgeSession(b.scope); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(b.ctx, b.bindDeadline)
	defer cancel()
	endpoint, err := prepareHostAgentEndpointContext(ctx, b.scope)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed || b.endpoint != nil {
		b.mu.Unlock()
		_ = endpoint.Close()
		return agentControlError()
	}
	b.endpoint = endpoint
	b.workers.Add(1)
	b.mu.Unlock()
	go b.acceptAgents(endpoint)
	if b.collectBinding {
		return b.probeNativeBinding()
	}
	return nil
}

func (b *hostAgentBridge) activate() error {
	if err := checkAgentBridgeSession(b.scope); err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed || b.active || b.endpoint == nil {
		b.mu.Unlock()
		return agentControlError()
	}
	endpoint := b.endpoint
	b.mu.Unlock()
	if err := endpoint.publish(b.ctx, b.scope.Session); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return agentControlError()
	}
	b.active = true
	return nil
}

func checkAgentBridgeSession(scope agentbridge.Scope) error {
	if err := scope.Check(); err != nil {
		return agentControlError()
	}
	if err := CheckCaller(); err != nil {
		return err
	}
	m, err := LoadManifest(scope.Session)
	if err != nil || m == nil || !m.SSHAgent || !manifestUnwalled(m) {
		return UserError("ssh_agent_refused", "this session does not allow the laptop's SSH agent",
			"Start an unwalled session with --ssh-agent on a host you own.")
	}
	return nil
}
