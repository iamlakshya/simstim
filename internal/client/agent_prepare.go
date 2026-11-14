package client

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/link"
)

func prepareAttachedAgent(ctx context.Context, probe *Conn, session string, interactive bool) (prepared *attachedAgent, err error) {
	mode, err := newAgentModeResources(ctx)
	if err != nil {
		return nil, err
	}
	keepMode := false
	defer func() {
		if !keepMode {
			cleanup, stop := context.WithTimeout(context.Background(), sshWaitDelay)
			defer stop()
			err = errors.Join(err, mode.close(cleanup))
		}
	}()
	target := os.Getenv("SSH_AUTH_SOCK")
	if err := probe.checkLiveAgentGrant(ctx, session, target, mode.inspector); err != nil {
		return nil, err
	}
	// One local zero-byte liveness check precedes preparation. Fresh policy
	// checks at publication and during the lease inspect metadata only.
	if err := checkLocalSSHAgentHealth(ctx, target); err != nil {
		return nil, err
	}
	ssh, ok := probe.route.t.(*SSH)
	if !ok {
		return nil, &UserError{Msg: "sharing the laptop's SSH agent needs a host reached over SSH", Hint: "Choose a host reached over SSH.", Code: "needs_ssh"}
	}
	lease, err := agentbridge.NewLease()
	if err != nil {
		return nil, err
	}
	scope := agentbridge.Scope{Session: session, Lease: lease}
	collector, err := newAgentBindingCollectorMode(ctx, scope, mode)
	if err != nil {
		return nil, err
	}
	defer func() {
		collector.Close()
		if failure := collector.log.Failure(); failure != nil && ctx.Err() == nil {
			if prepared != nil {
				prepared.Close()
				prepared = nil
			}
			err = agentNativeResourceError(failure)
		}
	}()
	collector.target = ssh.Host.SSH
	collector.hostID = probe.HostID()
	// Authentication keeps the user's original environment and identities.
	// The optional native forwarding socket is a public binding collector;
	// it cannot list, sign, or connect to the source agent.
	transport := *ssh
	transport.Interactive = interactive && InputAllowed(ctx)
	transport.ownConnection = true
	transport.bindingCollector = collector
	route := probe.route
	route.t, route.lc, route.agentOnly = &transport, nil, true
	conn, err := dial(ctx, probe.Host, route)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			conn.Close()
		}
	}()
	if conn.HostID() != probe.HostID() || conn.HostIDChanged() || !conn.HasFeature(agentbridge.Feature) {
		return nil, grantIdentityError(probe.Host)
	}
	a, err := bindAgentConn(ctx, conn, scope, target, collector, mode)
	if err != nil {
		return nil, err
	}
	keep, keepMode = true, true
	return a, nil
}

func bindAgentConn(ctx context.Context, conn *Conn, scope agentbridge.Scope, target string, collector *agentBindingCollector, mode *agentModeResources) (*attachedAgent, error) {
	if err := mode.checkAgentSettingsMode(ctx); err != nil {
		return nil, err
	}
	if err := scope.Check(); err != nil {
		return nil, agentChannelError("the agent lease scope is invalid")
	}
	live, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a := &attachedAgent{conn: conn, scope: scope, target: target, mode: mode,
		ctx: live, cancel: cancel, turn: make(chan struct{}, 1), slots: make(chan struct{}, agentbridge.MaxStreams),
		sources: map[net.Conn]struct{}{}, activation: make(chan struct{}), acceptDone: make(chan struct{}), done: make(chan struct{})}
	a.mux = link.New(agentbridge.NewConn(conn.agentReader, conn.stdin, conn.closeAgentTransport), link.Supervisor, agentbridge.Options())
	go a.accept()
	var rawExited <-chan struct{}
	if process, ok := conn.proc.(interface{ rawExited() <-chan struct{} }); ok {
		rawExited = process.rawExited()
	}
	go func() {
		select {
		case <-a.mux.Done():
			a.revoke(agentLinkError(conn, a.mux.Err()))
		case <-conn.Done():
			a.revoke(agentLinkError(conn, ErrClosed))
		case <-rawExited:
			// Existing source streams lose authority at actual process
			// exit even if an inherited pipe or final diagnostics stay live.
			// Diagnose only after revoking owned credential connections.
			a.revoke(ErrClosed)
		case <-live.Done():
		}
	}()
	ready := false
	defer func() {
		if !ready {
			a.Close()
		}
	}()
	header := struct {
		agentbridge.Scope
		CollectBinding bool `json:"collect_binding,omitempty"`
	}{Scope: a.scope, CollectBinding: collector != nil}
	control, err := a.mux.Open(ctx, link.KindCtl, header)
	a.ctl = control
	if err != nil {
		var old *link.VersionError
		if errors.As(err, &old) {
			return nil, conn.grantHelperOld("share an agent with one session")
		}
		return nil, err
	}
	if err := a.control(ctx, "bind", nil); err != nil {
		return nil, err
	}
	if collector != nil {
		a.binding, err = sealNativeAgentBinding(ctx, collector, conn)
		if err != nil {
			return nil, err
		}
	}
	a.ownsMode, ready = true, true
	return a, nil
}
