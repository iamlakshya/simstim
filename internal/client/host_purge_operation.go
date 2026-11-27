package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type purgeOperationV1 struct {
	mu                 sync.Mutex
	request            *GrantRequest
	conn               *Conn
	nativeTransport    *SSH
	nativeContinuation Transport
	nativeContext      context.Context
	nativeAttempt      *hostTransportAttempt
	nativeFinish       func()
	source             hostStateSourceWitness
	observation        *HostStateObservation
	receipt            purgeRemovalReceiptV1
	rootMode           RootStepMode
	rootPasswordless   bool
	rootConfigured     bool
	rootUndoSHA        string
	rootUndoRan        bool
	rootUndoSelected   bool
	active             bool
	busy               bool
	inFlight           int
}

func (p *purgeOperationV1) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.busy || p.inFlight != 0 {
		p.mu.Unlock()
		return &UserError{Msg: "the original purge still owns a native operation", Hint: "Wait for that operation to finish before closing its request.", Code: "purge_busy", Kind: KindRefused}
	}
	p.active = false
	finish, conn := p.nativeFinish, p.conn
	p.nativeFinish = nil
	p.mu.Unlock()
	if finish != nil {
		finish()
	}
	if conn != nil {
		conn.Close()
	}
	return nil
}

func (p *purgeOperationV1) check(ctx context.Context, c *Conn) error {
	if p == nil || ctx == nil {
		return grantOperationError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	p.mu.Lock()
	valid := p.active && p.request == r && p.conn == c
	if c != nil {
		valid = valid && p.receipt.HostID == c.HostID() && SameEntry(p.receipt.Original, c.Host)
	} else {
		valid = valid && p.nativeContinuation != nil && p.nativeContext != nil && p.nativeAttempt != nil
	}
	p.mu.Unlock()
	if !valid || r == nil {
		return grantOperationError()
	}
	r.mu.Lock()
	valid = r.purge == p && r.purpose == grantPurge && r.operation == grantOpPurge && !r.Untrusted && !r.SSHAgent
	life := r.lifetimeErrorLocked()
	r.mu.Unlock()
	if !valid || life != nil {
		return errors.Join(grantOperationError(), life)
	}
	ctx = WithHostStateObservation(ctx, p.observation)
	if err := requireRecordedHostStateObservation(ctx, p.receipt.Original, p.receipt.HostID, p.observation); err != nil {
		return err
	}
	if c != nil {
		return p.source.check(ctx, c)
	}
	if err := checkHostTransportAttempt(p.nativeContext, p.nativeAttempt, p.receipt.Original); err != nil {
		return err
	}
	original := hostStateSource{exists: p.source.Exists, generation: p.source.Generation, path: p.source.Path, root: p.source.Root}
	if !sameHostTransportSource(original, p.observation.source) {
		return grantOperationError()
	}
	return withHostMembershipContext(ctx, original.check)
}

// This hook is invoked by the ONE physical ordered request writer. A raw
// host.purge call with no original local operation is refused before writing.
func purgeBeforeRequest(ctx context.Context, c *Conn, request proto.Request, args any) error {
	if request.Op != "host.purge" && request.Op != "host.purge.commit" {
		return nil
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return grantOperationError()
	}
	r.mu.Lock()
	p := r.purge
	r.mu.Unlock()
	if err := p.check(ctx, c); err != nil {
		return err
	}
	p.mu.Lock()
	receipt := p.receipt
	p.mu.Unlock()
	switch request.Op {
	case "host.purge":
		supplied, ok := args.(api.PurgeArgs)
		if !ok || supplied.HostID != receipt.HostID || supplied.Nonce != receipt.Nonce || supplied.Force != receipt.Force || receipt.has("host") {
			return grantOperationError()
		}
	case "host.purge.commit":
		supplied, ok := args.(api.PurgeGuardWitness)
		if !ok || !receipt.readyForGuard() || receipt.Report.GuardWitness == nil {
			return grantOperationError()
		}
		expected, _ := json.Marshal(receipt.Report.GuardWitness)
		actual, _ := json.Marshal(supplied)
		if string(actual) != string(expected) {
			return grantOperationError()
		}
	}
	return nil
}

func (p *purgeOperationV1) checkRootUndoSourceV1(ctx context.Context, c *Conn, mode RootStepMode, options RootRunOptions) error {
	if err := p.check(ctx, c); err != nil {
		return err
	}
	p.mu.Lock()
	valid := p.rootConfigured && p.rootMode == mode && p.rootPasswordless == options.Passwordless && p.receipt.has("host") && !p.receipt.has("root")
	p.mu.Unlock()
	if !valid {
		return grantOperationError()
	}
	if mode != RootRun {
		return nil
	}
	operation, err := p.request.borrowNewSettings(ctx)
	if err != nil {
		return err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return err
	}
	tier, hid, err := grantHostTier(ctx, p.receipt.Original)
	if err != nil {
		return err
	}
	if hid != p.receipt.HostID || !min(tier, requestCeiling(ctx, tier)).Valid() {
		return grantPurposeError()
	}
	return nil
}

func (p *purgeOperationV1) bindRootUndoTemplateV1(ctx context.Context, c *Conn, sha string) error {
	if err := p.check(ctx, c); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.rootConfigured || !p.receipt.has("host") || p.receipt.has("root") || p.rootUndoSHA != "" && p.rootUndoSHA != sha {
		return grantOperationError()
	}
	p.rootUndoSHA = sha
	return nil
}

func grantRequestNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func (p *purgeOperationV1) retainNativeCallV1() (func(), error) {
	if p == nil {
		return nil, grantOperationError()
	}
	p.mu.Lock()
	if !p.active {
		p.mu.Unlock()
		return nil, grantOperationError()
	}
	p.inFlight++
	p.mu.Unlock()
	return sync.OnceFunc(func() { p.mu.Lock(); p.inFlight--; p.mu.Unlock() }), nil
}
