package client

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

// agentBindingCollector receives only public native forwarding bindings.
// It never opens an authentication/signing agent or proxies host requests.
type agentBindingCollector struct {
	scope      agentbridge.Scope
	target     string
	hostID     string
	socket     *agentCollectorSocket
	log        *agentNativeLog
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	owner      *sshProc
	ownerCtx   context.Context
	ownerPID   int
	ownerReady chan struct{}
	record     []byte
	err        error
	closed     bool
	connection net.Conn
	ready      chan struct{}
	readyOnce  sync.Once
	ownerOnce  sync.Once
	closeOnce  sync.Once
	done       chan struct{}
}

func newAgentBindingCollector(ctx context.Context, scope agentbridge.Scope) (*agentBindingCollector, error) {
	return newAgentBindingCollectorMode(ctx, scope, nil)
}

func newAgentBindingCollectorMode(ctx context.Context, scope agentbridge.Scope, mode *agentModeResources) (*agentBindingCollector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scope.Check(); err != nil {
		return nil, agentBindingProofError()
	}
	var socket *agentCollectorSocket
	var log *agentNativeLog
	var err error
	if mode == nil {
		socket, err = newAgentCollectorSocket()
	} else {
		if err = mode.checkAgentSettingsMode(ctx); err != nil {
			return nil, err
		}
		socket, err = mode.newCollectorSocket(ctx)
	}
	if err != nil {
		return nil, err
	}
	if mode == nil {
		log, err = newAgentNativeLog(ctx)
	} else {
		log, err = mode.newNativeLog(ctx)
	}
	if err != nil {
		socket.Close()
		return nil, err
	}
	live, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &agentBindingCollector{scope: scope, socket: socket, log: log, ctx: live, cancel: cancel,
		ownerReady: make(chan struct{}), ready: make(chan struct{}), done: make(chan struct{})}
	go c.accept()
	return c, nil
}

func (c *agentBindingCollector) attachOwner(owner *sshProc, ctx context.Context, program *nativeAgentProgram) error {
	raw, ok := owner.Proc.(*cmdProc)
	if !ok || raw.cmd.Process == nil || owner.rawDone == nil || !sameNativeAgentProgram(program, raw.cmd.Path) {
		return agentBindingProofError()
	}
	attached := false
	var attachError error
	c.ownerOnce.Do(func() {
		c.mu.Lock()
		c.owner, c.ownerCtx, c.ownerPID = owner, ctx, raw.cmd.Process.Pid
		c.mu.Unlock()
		if err := c.log.AttachOwner(func(reason error) { c.fail(reason); owner.Kill() }); err != nil {
			attachError = err
			c.fail(err)
			owner.Kill()
			return
		}
		c.mu.Lock()
		closed := c.closed || c.err != nil
		c.mu.Unlock()
		if closed {
			attachError = agentBindingProofError()
			owner.Kill()
			return
		}
		// The reader may verify only after failures can revoke the exact
		// owned process. Startup log ownership precedes this publication.
		close(c.ownerReady)
		attached = true
	})
	if !attached {
		if attachError != nil {
			return attachError
		}
		return agentBindingProofError()
	}
	return nil
}

func (c *agentBindingCollector) accept() {
	defer close(c.done)
	connection, err := c.socket.listener.Accept()
	// There is one attempt per lease, never a retry with later host data.
	_ = c.socket.listener.Close()
	if err != nil {
		c.fail(err)
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = connection.Close()
		return
	}
	c.connection = connection
	c.mu.Unlock()
	defer func() { _ = connection.Close(); c.mu.Lock(); c.connection = nil; c.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(c.ctx, agentbridge.HandshakeTimeout)
	defer cancel()
	select {
	case <-c.ownerReady:
	case <-ctx.Done():
		c.fail(ctx.Err())
		return
	}
	c.mu.Lock()
	ownerCtx, pid, owner := c.ownerCtx, c.ownerPID, c.owner
	c.mu.Unlock()
	if owner == nil || owner.rawDone == nil {
		c.fail(agentBindingProofError())
		return
	}
	select {
	case <-ownerCtx.Done():
		c.fail(agentBindingProofError())
		return
	default:
	}
	select {
	case <-owner.rawExited():
		c.fail(agentBindingProofError())
		return
	default:
	}
	peerPID, peerUID, err := agentCollectorPeer(connection)
	if err != nil || peerPID != pid || !agentCollectorOwnUID(peerUID) {
		c.fail(agentBindingProofError())
		return
	}
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		c.fail(err)
		return
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	var header [4]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		c.fail(agentBindingProofError())
		return
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > agentFWMaxBody {
		c.fail(agentBindingProofError())
		return
	}
	wire := make([]byte, int(n)+4)
	copy(wire, header[:])
	if _, err := io.ReadFull(connection, wire[4:]); err != nil {
		c.fail(agentBindingProofError())
		return
	}
	// The owned native hook is now waiting for this reply. Synchronise all
	// earlier writes on its separate diagnostics FD before acknowledging.
	peer, err := readAgentNativePeer(ctx, c.log)
	if err != nil {
		c.fail(err)
		return
	}
	if err := verifyNativeAgentRecord(wire, peer); err != nil {
		c.fail(err)
		return
	}
	select {
	case <-ownerCtx.Done():
		c.fail(agentBindingProofError())
		return
	default:
	}
	select {
	case <-owner.rawExited():
		c.fail(agentBindingProofError())
		return
	default:
	}
	if err := writeAgentFirewallBytes(connection, []byte{0, 0, 0, 1, agentFWSuccess}); err != nil {
		c.fail(err)
		return
	}
	c.mu.Lock()
	if c.closed || c.err != nil {
		c.mu.Unlock()
		return
	}
	c.record = wire
	c.mu.Unlock()
	c.readyOnce.Do(func() { close(c.ready) })
}

func (c *agentBindingCollector) fail(err error) {
	if err == nil {
		err = agentBindingProofError()
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.record = nil
	connection := c.connection
	c.mu.Unlock()
	c.cancel()
	_ = c.socket.listener.Close()
	if connection != nil {
		_ = connection.Close()
	}
	c.readyOnce.Do(func() { close(c.ready) })
}

func (c *agentBindingCollector) wait(ctx context.Context) error {
	select {
	case <-c.ready:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.closed || len(c.record) == 0 {
		return agentBindingProofError()
	}
	return nil
}

func (c *agentBindingCollector) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		connection, owned := c.connection, c.owner != nil
		c.record = nil
		c.mu.Unlock()
		c.cancel()
		_ = c.socket.listener.Close()
		if connection != nil {
			_ = connection.Close()
		}
		// The accept/verifier uses only its connection and the separate log
		// after Accept. No collector root is borrowed by that reader.
		c.socket.Close()
		select {
		case <-c.done:
		case <-time.After(sshWaitDelay):
			c.log.fail(agentNativeLogError("the native binding verifier did not stop"))
		}
		if !owned {
			_ = c.log.Close()
		}
	})
}

func agentNativeResourceError(err error) error {
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	return &UserError{Msg: "the private agent connection diagnostics could not be kept safely", Hint: "Check this computer's temporary folder and SSH diagnostics, then attach again.", Code: "ssh_agent_refused", Kind: KindRefused}
}
