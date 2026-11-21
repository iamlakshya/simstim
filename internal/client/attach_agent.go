package client

import (
	"context"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type agentReady struct {
	agent *attachedAgent
	err   error
}

// dialAttach leaves the ordinary terminal connection unforwarded. Optional
// agent preparation owns another, nonshared connection with its own reader.
func dialAttachTransport(ctx context.Context, h Host, session string, interactive bool) (connection *Conn, prepared *attachedAgent, returned error) {
	if err := checkSessionID(h.Name, session); err != nil {
		return nil, nil, err
	}
	conn, err := Dial(ctx, h, interactive)
	if err != nil {
		return nil, nil, err
	}
	mode, err := newAgentModeResources(ctx)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	defer func() {
		if mode != nil {
			if err := mode.closeInactive(); err != nil {
				conn.Close()
				connection, prepared = nil, nil
				returned = errors.Join(returned, err)
			}
		}
	}()
	_, policy, err := effectiveAgentSessionTier(ctx, h, session, conn.HostID(), mode)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if policy == nil || policy.FutureLineage || policy.FutureDelivery || !policy.SSHAgent {
		return conn, nil, nil
	}
	// Transfer this same real mode to preparation; it owns cleanup on
	// failure and the resulting attached agent owns it on success.
	ownedMode := mode
	mode = nil
	agent, err := prepareAttachedAgentMode(ctx, conn, session, interactive, ownedMode)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, agent, nil
}

func (c *Conn) checkAgentGrant(ctx context.Context, session, target string, settings *agentSettingsInspector) error {
	if err := c.checkAgentPolicy(ctx, session, settings); err != nil {
		return err
	}
	return checkLocalSSHAgentPath(target)
}

func (c *Conn) checkAgentPolicy(ctx context.Context, session string, settings *agentSettingsInspector) error {
	return settings.allowSession(ctx, c, session)
}

func (c *Conn) checkLiveAgentGrant(ctx context.Context, session, target string, settings *agentSettingsInspector) error {
	ctx, release, err := c.beginGrant(ctx, "attach", true)
	if err != nil {
		return err
	}
	defer release()
	return c.checkAgentGrant(ctx, session, target, settings)
}

func (c *Conn) sendAttach(ctx context.Context, req proto.Attach) error {
	// Agent capability has its own connection and explicit activation.
	req.ForwardAgent = false
	return c.w.WriteJSONCtx(ctx, proto.TAttach, req)
}

func (a *attacher) startAgent(ctx context.Context) {
	if a.agent == nil {
		return
	}
	agent := a.agent
	ready := make(chan agentReady, 1)
	a.agentReady, a.agentStarting = ready, true
	a.agentSetup.Add(1)
	go func() {
		defer a.agentSetup.Done()
		ready <- agentReady{agent: agent, err: agent.Activate(ctx)}
	}()
}

func (a *attacher) agentDone() <-chan struct{} {
	if a.agent == nil || a.agentStarting {
		return nil
	}
	return a.agent.Done()
}

func (a *attacher) stopAgent() {
	agent := a.agent
	a.agent, a.agentReady, a.agentStarting = nil, nil, false
	if agent != nil {
		agent.Close()
	}
	a.agentSetup.Wait()
}

func (a *attacher) closeConnections() { a.stopAgent(); a.conn.Close() }

func (a *attacher) watchAgentGrant(ctx context.Context) error {
	if a.agent == nil || !a.agent.isActive() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := a.agent.checkLiveGrant(ctx, a.o.Session, a.agent.target); err != nil {
		// A host operation may have committed a downgrade or removal
		// before releasing its operation lock. Busy cannot renew stale
		// credential authority while that operation finishes.
		a.agent.CloseWithError(err)
		return err
	}
	if !a.agentStarting {
		a.agent.tick(ctx)
	}
	return nil
}

func (a *attacher) onAgentReady(ctx context.Context, ready agentReady, held *[]byte, detachTimer, flushTimer *<-chan time.Time) (bool, error) {
	a.agentReady, a.agentStarting = nil, false
	if ready.agent != a.agent {
		ready.agent.Close()
		return false, nil
	}
	if ready.err != nil {
		return false, ready.err
	}
	hadHeld := len(*held) > 0
	if hadHeld {
		b := *held
		*held = nil
		if a.onKeys(ctx, b, true, detachTimer, flushTimer) {
			return true, nil
		}
	}
	if hadHeld || !a.inputPending {
		a.askInput()
	}
	return false, nil
}

func (a *attacher) onAgentLost() error {
	previous := a.agent
	err := previous.Err()
	a.stopAgent()
	err = agentLinkError(previous.conn, err)
	if genuineAgentRevocation(err) {
		return err
	}
	a.conn.kill()
	return nil
}

func (a *attacher) askInput() {
	if !a.inputPending {
		a.in.ask()
		a.inputPending = true
	}
}
