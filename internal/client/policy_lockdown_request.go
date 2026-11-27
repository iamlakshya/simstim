package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// PolicyLockdownRequestV1 retains one explicit host restriction change. It
// reads no policy file, selects no rules or values and creates no allowance.
type PolicyLockdownRequestV1 struct {
	mu                      sync.Mutex
	request                 *GrantRequest
	host                    Host
	on                      bool
	conn                    *Conn
	stamp                   *hostTransportStamp
	hostID                  string
	tier                    Tier
	busy, attempted, closed bool
}
type policyLockdownPublicationKeyV1 struct{}

func (*PolicyLockdownRequestV1) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "<host lockdown request>")
}

// NewPolicyLockdownRequestV1 captures the literal off intent before Dial. The
// word belongs only to this local action and never becomes an RPC permission.
func NewPolicyLockdownRequestV1(host Host, on bool, literal string) (*PolicyLockdownRequestV1, error) {
	if !ValidHostName(host.Name) {
		return nil, badHostNameError(host.Name)
	}
	if err := validateLockdownLiteralV1(on, literal); err != nil {
		return nil, err
	}
	return &PolicyLockdownRequestV1{request: &GrantRequest{purpose: grantRemote, operation: grantOpPolicyReplace}, host: cloneGrantHost(host), on: on, tier: TierOwn}, nil
}

func validateLockdownLiteralV1(on bool, literal string) error {
	if !on && literal != "lockdown" {
		return &UserError{Msg: "lifting lockdown needs the literal word lockdown", Hint: "Type lockdown at the question, or pass --confirm=lockdown.", Code: "lockdown_confirm", Kind: KindNeedsAnswer}
	}
	if on && literal != "" {
		return &UserError{Msg: "the confirmation word belongs to lifting lockdown", Hint: "Use --off --confirm=lockdown, or leave --confirm out when enabling lockdown.", Code: "lockdown_confirm", Kind: KindUsage}
	}
	return nil
}

func (p *PolicyLockdownRequestV1) Context(ctx context.Context) context.Context {
	if p == nil || p.request == nil {
		return ctx
	}
	return context.WithValue(WithGrantRequest(ctx, p.request), policyLockdownPublicationKeyV1{}, p)
}

func (p *PolicyLockdownRequestV1) check(ctx context.Context, c *Conn) error {
	if p == nil || p.request == nil || c == nil || ctx == nil || grantRequestOf(ctx) != p.request {
		return policyLockdownSourceErrorV1()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.request.mu.Lock()
	lifetime := p.request.lifetimeErrorLocked()
	valid := p.request.purpose == grantRemote && p.request.operation == grantOpPolicyReplace && !p.request.Untrusted && !p.request.SSHAgent
	p.request.mu.Unlock()
	if lifetime != nil {
		return lifetime
	}
	if !valid {
		return policyLockdownSourceErrorV1()
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return err
	}
	if c.agentOnly || c.hostTransport.kind != hostTransportSaved || !SameEntry(p.host, c.Host) || !ValidHostID(c.HostID()) || c.HostIDChanged() {
		return policyLockdownSourceErrorV1()
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return err
	}
	tier, hostID, err := HostGrantTier(WithTierQuestion(ctx, nil), p.host)
	if err != nil {
		return err
	}
	if !tier.Valid() || hostID != c.HostID() {
		return grantIdentityError(p.host)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return policyLockdownSourceErrorV1()
	}
	if p.conn != nil && (p.conn != c || p.stamp != c.hostTransport || p.hostID != c.HostID()) {
		return policyLockdownSourceErrorV1()
	}
	p.conn, p.stamp, p.hostID = c, c.hostTransport, c.HostID()
	p.tier = min(p.tier, tier, requestCeiling(ctx, tier))
	return nil
}

func policyLockdownSourceErrorV1() error {
	return &UserError{Msg: "the lockdown change lost its original host, source or local intent", Hint: "Select the saved host again and start a fresh lockdown action.", Code: "policy_sources", Kind: KindRefused}
}

func policyLockdownBeforeRequestV1(ctx context.Context, c *Conn, req proto.Request, input any) error {
	if req.Op != "policy.lockdown" {
		return nil
	}
	if ctx == nil || c == nil {
		return policyLockdownSourceErrorV1()
	}
	p, _ := ctx.Value(policyLockdownPublicationKeyV1{}).(*PolicyLockdownRequestV1)
	if p == nil {
		return policyLockdownSourceErrorV1()
	}
	if err := checkPolicyChange(ctx); err != nil {
		return err
	}
	held, ok := ctx.Value(heldKey{c.Host.Name}).(*heldLock)
	if !ok || held.gone.Load() {
		return policyLockdownSourceErrorV1()
	}
	if err := p.check(ctx, c); err != nil {
		return err
	}
	expected := api.PolicyLockdownArgsV1{Version: 1, On: p.on}
	encoded, err := json.Marshal(expected)
	if err != nil || !reflect.DeepEqual(input, expected) || !bytes.Equal(encoded, req.Args) {
		return policyLockdownSourceErrorV1()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.busy || p.closed || p.attempted {
		return policyLockdownSourceErrorV1()
	}
	p.attempted = true
	return nil
}

// Apply executes only the captured restriction change on the same accepted
// connection. Off recompiles the host's current protected policy; it never
// reads laptop layers, fetches values or repeats another operation's grants.
func (p *PolicyLockdownRequestV1) Apply(ctx context.Context, c *Conn) (api.HostLockdownResultV1, error) {
	var result api.HostLockdownResultV1
	if err := p.check(ctx, c); err != nil {
		return result, err
	}
	if !c.HasFeature(api.FeaturePolicyLockdownV1) {
		return result, c.grantHelperOld("apply host lockdown with its actual policy actor")
	}
	if err := checkPolicyChange(ctx); err != nil {
		return result, err
	}
	p.mu.Lock()
	if p.busy || p.closed || p.attempted {
		p.mu.Unlock()
		return result, policyLockdownSourceErrorV1()
	}
	p.busy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	owned, release, err := c.beginGrantSource(ctx, "change host lockdown", true)
	if err != nil {
		return result, err
	}
	defer release()
	if err := p.check(owned, c); err != nil {
		return result, err
	}
	id, ch, err := c.startCall(owned, "policy.lockdown", api.PolicyLockdownArgsV1{Version: 1, On: p.on})
	if err != nil {
		return result, err
	}
	release()
	var raw json.RawMessage
	callErr := c.awaitCallResult(owned, "policy.lockdown", id, ch, &raw, true)
	if len(raw) == 0 {
		if callErr == nil {
			callErr = policyLockdownSourceErrorV1()
		}
		return result, callErr
	}
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Validate() != nil || result.On != p.on {
		return api.HostLockdownResultV1{}, &UserError{Msg: "the host returned an invalid lockdown report", Hint: "Use matching helpers and inspect the host's current lockdown state.", Code: "policy_protocol", Kind: KindRefused}
	}
	if err := p.check(ctx, c); err != nil {
		return result, err
	}
	if len(result.Unreached) > 0 && callErr == nil {
		callErr = &UserError{Msg: "lockdown did not reach every sandbox on the host", Hint: "Retry after the unreachable sandbox is ready.", Code: "policy_unreached", Kind: KindFailed}
	}
	return result, callErr
}

func (p *PolicyLockdownRequestV1) Close() error {
	if p == nil || p.request == nil {
		return nil
	}
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return policyLockdownSourceErrorV1()
	}
	p.closed = true
	p.mu.Unlock()
	return p.request.Close()
}

func policyLockdownEntryHashV1(host Host) (string, error) {
	raw, err := json.Marshal(entryOf(host))
	if err != nil {
		return "", policyLockdownSourceErrorV1()
	}
	sum := sha256.Sum256(raw)
	clear(raw)
	return hex.EncodeToString(sum[:]), nil
}

func policyLockdownClientErrorV1(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	return &UserError{Msg: "the lockdown change could not be completed: " + CleanLine(err.Error()), Hint: "Keep the host saved and retry against its current helper.", Code: "policy_unreached", Kind: KindFailed}
}
