package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

type newPublicationKey struct{}

// This witness exists only around the actual pending RPC. Its wire digest is
// ephemeral and never enters receipt/state/history. No decoded DTO creates it.
type newPublicationWitness struct {
	base                         context.Context
	plan                         *newSecretPlan
	conn                         *Conn
	creation                     *SandboxPolicy
	args                         NewArgs
	source                       hostStateSourceWitness
	requestID                    uint64
	wire                         [32]byte
	prepared, sent, acknowledged bool
	receipt                      *sandboxDeliveryReceipt
}

func newPublicationContext(base, owned context.Context, c *Conn, p *newSecretPlan, a NewArgs) (context.Context, error) {
	if p == nil || p.intent == nil {
		return owned, nil
	}
	source, err := captureHostStateSourceWitness(owned, c)
	if err != nil {
		return owned, err
	}
	w := &newPublicationWitness{base: base, plan: p, conn: c, creation: cloneSandboxPolicy(p.creation), args: a, source: source}
	return context.WithValue(owned, newPublicationKey{}, w), nil
}

func (w *newPublicationWitness) beforeRequest(ctx context.Context, c *Conn, req proto.Request, args any) error {
	if w.prepared || c != w.conn || req.Op != "new" || req.ID == 0 {
		return sandboxReceiptError()
	}
	actual, ok := args.(NewArgs)
	if !ok || !reflect.DeepEqual(actual, w.args) || !sameSelectedSecretValues(actual.Secrets, w.plan.values) || actual.ID != w.creation.SessionID || !reflect.DeepEqual(actual.SecretDelivery, w.plan.intent) {
		return sandboxReceiptError()
	}
	if err := w.plan.operation.Revalidate(ctx); err != nil {
		return err
	}
	if err := w.source.check(ctx, c); err != nil {
		return err
	}
	// The already encoded body is reused by the actual writer. Count the
	// actual ID/header without re-encoding/copying all secret-bearing args.
	header, err := json.Marshal(proto.Request{ID: req.ID, Op: req.Op, Args: json.RawMessage("{}")})
	if err != nil || len(req.Args)+len(header)-2+1 > proto.MaxFrame {
		return &UserError{Msg: "the complete session request exceeds the protocol frame", Hint: "Select fewer inputs or grants for this start.", Code: "state_too_large", Kind: KindRefused}
	}
	tier, _, err := c.newGrantTier(ctx, w.plan.selection.names, actual.SSHAgent)
	if err != nil {
		return err
	}
	if err := recordSecretNames(ctx, c.Host, c.HostID(), tier, actual.Secrets); err != nil {
		return err
	}
	w.requestID = req.ID
	w.wire = sha256.Sum256(req.Args)
	w.prepared = true
	return nil
}

func (w *newPublicationWitness) wroteRequest(c *Conn, id uint64) {
	if w.prepared && c == w.conn && id == w.requestID {
		w.sent = true
	}
}

func (w *newPublicationWitness) afterResponse(ctx context.Context, c *Conn, response proto.Response, out any) error {
	if !w.prepared || !w.sent || w.acknowledged || c != w.conn || response.ID != w.requestID || !response.OK {
		return sandboxReceiptError()
	}
	s, ok := out.(*Session)
	if !ok || s == nil || s.ID != w.creation.SessionID || s.Agent != w.plan.selection.Kind() || s.SandboxOf != "" || !ValidHostID(s.RunGeneration) || s.SecretState == nil {
		return sandboxReceiptError()
	}
	if err := egress.ValidateSavedPublicSessionState(s.SecretState); err != nil {
		return err
	}
	if err := egress.ValidateAcknowledgedSessionSecretState(ctx, w.plan.selection.Kind(), *w.plan.intent, w.args.Secrets, s.SecretState); err != nil {
		return err
	}
	if !reflect.DeepEqual(s.SecretState.Intent, *w.plan.intent) {
		return sandboxReceiptError()
	}
	allowed := append([]string{}, w.plan.selection.names...)
	domain := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		domain[name] = true
	}
	state, err := egress.CloneLocalSessionSecretState(s.SecretState, domain)
	if err != nil {
		return err
	}
	supplied := append([]string{}, secretMapNames(w.args.Secrets)...)
	missing := []string{}
	for _, name := range allowed {
		if !slices.Contains(supplied, name) {
			missing = append(missing, name)
		}
	}
	actualNames, err := canonicalGrantNames(s.Secrets)
	if err != nil || !slices.Equal(actualNames, allowed) {
		return sandboxReceiptError()
	}
	actualMissing, err := canonicalGrantNames(s.MissingSecrets)
	if err != nil || !slices.Equal(actualMissing, missing) {
		return sandboxReceiptError()
	}
	revision, err := newLocalReceiptID()
	if err != nil {
		return err
	}
	publication, err := newLocalReceiptID()
	if err != nil {
		return err
	}
	receipt := &sandboxDeliveryReceipt{Version: 1, Revision: revision, Creation: w.creation.request, Publication: publication, Operation: "new",
		Scope:   sandboxReceiptScope{Host: c.Host.Name, Source: w.source.clone(), HostID: c.HostID(), MemberID: s.ID, RootID: s.ID, RunGeneration: s.RunGeneration, BackendGeneration: state.Generation},
		Allowed: allowed, Supplied: supplied, Missing: missing, State: state}
	w.receipt = cloneSandboxDeliveryReceipt(receipt)
	w.acknowledged = true
	if err := storeSandboxDeliveryReceiptContext(w.base, c, w.creation, "", receipt, w); err != nil {
		return err
	}
	w.plan.mu.Lock()
	w.plan.published = true
	w.plan.mu.Unlock()
	return nil
}

func (w *newPublicationWitness) checkSandboxReceiptWrite(ctx context.Context, intent *sandboxReceiptWriteIntent) error {
	if intent == nil || intent.kind != deliveryPublicationReceipt || intent.conn != w.conn || !w.prepared || !w.sent || !w.acknowledged || w.requestID == 0 || w.receipt == nil ||
		intent.expectedRevision != "" || !sameSandboxCreation(intent.original, w.creation) || !reflect.DeepEqual(intent.delivery, w.receipt) {
		return sandboxReceiptError()
	}
	if err := w.plan.operation.Revalidate(ctx); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, w.conn); err != nil {
		return err
	}
	return w.source.check(ctx, w.conn)
}

func newLocalReceiptID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", &UserError{Msg: "could not identify this local publication receipt", Hint: "Keep the session and retry the local operation.", Code: "operation_receipt", Kind: KindFailed}
	}
	return hex.EncodeToString(nonce[:]), nil
}
