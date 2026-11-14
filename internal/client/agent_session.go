package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/link"
)

// attachedAgent owns a separate agent-only bridge. Its raw transport close
// never joins the link reader. The attacher joins everything after revoke.
type attachedAgent struct {
	conn     *Conn
	mode     *agentModeResources
	ownsMode bool
	scope    agentbridge.Scope
	target   string
	binding  *peerAgentBinding
	mux      *link.Session
	ctl      *link.Stream
	ctx      context.Context
	cancel   context.CancelFunc
	turn     chan struct{}
	seq      uint64

	mu             sync.Mutex
	active, closed bool
	activating     bool
	activation     chan struct{}
	pinging        bool
	err            error
	sources        map[net.Conn]struct{}
	slots          chan struct{}
	workers        sync.WaitGroup
	acceptDone     chan struct{}
	done           chan struct{}
	revokeOnce     sync.Once
	closeOnce      sync.Once
}

func (a *attachedAgent) control(ctx context.Context, action string, published func()) error {
	ctx, cancel := context.WithTimeout(ctx, agentbridge.HandshakeTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case a.turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		return a.Err()
	}
	defer func() { <-a.turn }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.seq == math.MaxUint64 {
		return agentChannelError("the agent channel sequence is exhausted")
	}
	a.seq++
	req := agentbridge.Request{Scope: a.scope, Action: action, Seq: a.seq}
	deadline := time.Now().Add(agentbridge.HandshakeTimeout)
	if due, ok := ctx.Deadline(); ok && due.Before(deadline) {
		deadline = due
	}
	if err := a.ctl.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set agent control deadline: %w", err)
	}
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(cancelDone); _ = a.ctl.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-cancelDone
		}
		_ = a.ctl.SetDeadline(time.Time{})
	}()
	if err := agentbridge.WriteMessage(a.ctl, req); err != nil {
		return err
	}
	if err := a.ctl.Flush(ctx); err != nil {
		return err
	}
	if published != nil {
		published()
	}
	return a.readControlReply(ctx, action, req.Seq)
}

func (a *attachedAgent) readControlReply(ctx context.Context, action string, seq uint64) error {
	var reply agentbridge.Reply
	if err := agentbridge.ReadMessage(a.ctl, &reply); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, link.ErrLinkClosed) {
			return err
		}
		return agentChannelError("the host sent invalid agent channel control data")
	}
	if reply.Scope != a.scope || reply.Action != action || reply.Seq != seq || reply.Seq == 0 {
		return agentChannelError("the host answered for a different agent lease")
	}
	if !reply.OK {
		if reply.Error != nil {
			return hostErrorOn(a.conn.Host.Name, reply.Error)
		}
		return agentChannelError("the host refused this agent channel")
	}
	if reply.Error != nil {
		return agentChannelError("the host sent an ambiguous agent answer")
	}
	return nil
}

// Activate checks local policy again after the exact terminal attach. The
// ledger and policy lock cover actual activation publication, not queuing.
func (a *attachedAgent) Activate(ctx context.Context) error {
	if !a.binding.matches(a.conn, a.scope) {
		err := agentChannelError("a trusted outer agent binding is not available")
		a.CloseWithError(err)
		return err
	}
	a.mu.Lock()
	if a.closed || a.active || a.activating {
		a.mu.Unlock()
		err := agentChannelError("this agent lease was already activated or closed")
		a.CloseWithError(err)
		return err
	}
	a.activating = true
	a.mu.Unlock()
	ctx, release, err := a.conn.beginGrant(ctx, "attach", true)
	if err != nil {
		a.revoke(err)
		return err
	}
	defer release()
	abort := func(err error) error {
		// The physical transport close drains publication users. This
		// caller must release its own hold before entering that drain.
		release()
		a.CloseWithError(err)
		return err
	}
	if err := a.conn.checkAgentPolicy(ctx, a.scope.Session, a.mode.inspector); err != nil {
		return abort(err)
	}
	if err := checkLocalSSHAgentPath(a.target); err != nil {
		return abort(err)
	}
	if err := RecordGrantContext(ctx, a.conn.Host, a.conn.HostID(), Outbound{Kind: OutboundSSHAgent}); err != nil {
		return abort(err)
	}
	// Correctly scoped streams can wait, bounded by their sixteen slots.
	// No source connection opens until the activation bytes are published.
	published := func() {
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return
		}
		a.active, a.activating = true, false
		close(a.activation)
		a.mu.Unlock()
		release()
	}
	if err := a.control(ctx, "activate", published); err != nil {
		return abort(err)
	}
	return nil
}

// Ping renews the exact active lease on the single control stream.
func (a *attachedAgent) Ping(ctx context.Context) error {
	if err := a.control(ctx, "ping", nil); err != nil {
		a.revoke(err)
		return err
	}
	return nil
}

func (a *attachedAgent) accept() {
	defer close(a.acceptDone)
	for {
		stream, err := a.mux.Accept(a.ctx)
		if err != nil {
			a.revoke(agentLinkError(a.conn, err))
			return
		}
		var header agentbridge.Header
		if stream.Kind() != link.KindAgent || stream.Header(&header) != nil || header.Kind != string(link.KindAgent) || header.Scope != a.scope {
			a.revoke(agentChannelError("the host opened an agent stream for a different lease"))
			return
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			rejectAgentStream(stream)
			continue
		}
		select {
		case a.slots <- struct{}{}:
			a.workers.Add(1)
			a.mu.Unlock()
			go a.serve(stream)
		default:
			a.mu.Unlock()
			rejectAgentStream(stream)
		}
	}
}

func rejectAgentStream(stream *link.Stream) {
	_ = stream.SetWriteDeadline(time.Now())
	_ = stream.Close()
}

func (a *attachedAgent) serve(stream *link.Stream) {
	defer a.workers.Done()
	defer func() { <-a.slots }()
	defer func() { _ = stream.Close() }()
	select {
	case <-a.activation:
	case <-a.ctx.Done():
		return
	}
	if !a.binding.matches(a.conn, a.scope) {
		a.revoke(agentChannelError("a trusted outer agent binding is not available"))
		return
	}
	d := net.Dialer{Timeout: time.Second}
	source, err := d.DialContext(a.ctx, "unix", a.target)
	if err != nil {
		return
	}
	a.mu.Lock()
	if !a.active || a.closed {
		a.mu.Unlock()
		_ = source.Close()
		return
	}
	a.sources[source] = struct{}{}
	a.mu.Unlock()
	defer func() {
		_ = source.Close()
		a.mu.Lock()
		delete(a.sources, source)
		a.mu.Unlock()
	}()
	if err := forwardAgentAuthentication(a.ctx, stream, source, a.binding); err != nil && isAgentSourceFailure(err) {
		a.revoke(agentChannelError("the local SSH agent did not accept its forwarding binding or protocol"))
	}
}

func (a *attachedAgent) revoke(err error) {
	a.revokeOnce.Do(func() {
		a.mu.Lock()
		a.active, a.closed, a.err = false, true, err
		a.cancel()
		if a.mode != nil {
			a.mode.cancel()
		}
		// Close all signing sockets before waiting for a stream or process.
		for source := range a.sources {
			_ = source.Close()
		}
		a.mu.Unlock()
		_ = a.conn.closeAgentTransport()
		close(a.done)
	})
}

// Done closes after every local signing socket has been revoked.
func (a *attachedAgent) Done() <-chan struct{} { return a.done }

// Err is the recorded reason this channel ended.
func (a *attachedAgent) Err() error {
	a.mu.Lock()
	err := a.err
	a.mu.Unlock()
	if err == nil {
		err = ErrClosed
	}
	if a.conn != nil {
		return agentLinkError(a.conn, err)
	}
	return err
}

// CloseWithError revokes first, then joins only independently owned workers.
func (a *attachedAgent) CloseWithError(err error) {
	if a == nil {
		return
	}
	a.revoke(err)
	a.closeOnce.Do(func() {
		_ = a.mux.Close()
		<-a.acceptDone
		a.workers.Wait()
		a.conn.Close()
		if a.ownsMode {
			cleanup, stop := context.WithDeadline(context.Background(), a.conn.agentStoppedAt.Add(3*sshWaitDelay))
			modeError := a.mode.close(cleanup)
			stop()
			if modeError != nil {
				a.mu.Lock()
				a.err = errors.Join(a.err, modeError)
				a.mu.Unlock()
			}
		}
		// Raw exit may precede bounded diagnostic finalisation. Preserve any
		// genuine earlier refusal, but upgrade EOF/ErrClosed after owned join.
		a.mu.Lock()
		before := a.err
		a.mu.Unlock()
		after := agentLinkError(a.conn, before)
		a.mu.Lock()
		if !genuineAgentRevocation(a.err) {
			a.err = after
		}
		a.mu.Unlock()
	})
}

// Close ends an attachment lease and joins its process and workers.
func (a *attachedAgent) Close() { a.CloseWithError(net.ErrClosed) }

func agentChannelError(msg string) error {
	return &UserError{Msg: msg, Hint: "Attach again after checking the host and this computer's SSH agent.", Code: "ssh_agent_refused", Kind: KindRefused}
}

func genuineAgentRevocation(err error) bool {
	var ue *UserError
	return errors.As(err, &ue)
}

func (a *attachedAgent) tick(ctx context.Context) {
	a.mu.Lock()
	if a.closed || !a.active || a.pinging {
		a.mu.Unlock()
		return
	}
	a.pinging = true
	a.workers.Add(1)
	a.mu.Unlock()
	// The heartbeat waits off the terminal loop and owns its short context.
	go func() {
		defer a.workers.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = a.Ping(ctx)
		a.mu.Lock()
		a.pinging = false
		a.mu.Unlock()
	}()
}

func (a *attachedAgent) isActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active && !a.closed
}

func agentLinkError(conn *Conn, err error) error {
	if genuineAgentRevocation(err) {
		return err
	}
	if source, ok := conn.proc.(interface{ agentFailure() error }); ok {
		if failure := source.agentFailure(); failure != nil {
			return failure
		}
	}
	var old *link.VersionError
	if errors.As(err, &old) {
		return conn.grantHelperOld("share an agent with one session")
	}
	if errors.Is(err, link.ErrBreach) {
		return agentChannelError("the host broke the agent channel protocol")
	}
	return err
}
