package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

type freshChildPublicationKey struct{}

type freshChildPublicationWitness struct {
	base                         context.Context
	plan                         *freshChildPlan
	conn                         *Conn
	creation                     *SandboxPolicy
	args                         api.JoinArgs
	source                       hostStateSourceWitness
	requestID                    uint64
	wire                         [32]byte
	prepared, sent, acknowledged bool
	receipt                      *sandboxDeliveryReceipt
}

func (w *freshChildPublicationWitness) beforeRequest(ctx context.Context, c *Conn, req proto.Request, arguments any) error {
	actual, ok := arguments.(api.JoinArgs)
	if w.prepared || c != w.conn || req.Op != "spawn" || req.ID == 0 || !ok ||
		!reflect.DeepEqual(actual, w.args) || actual.ID != w.creation.SessionID ||
		!sameSelectedSecretValues(actual.Secrets, w.plan.values) || !reflect.DeepEqual(actual.SecretDelivery, &w.plan.intent) ||
		!sameGrantSnapshot(actual.Snapshot, w.plan.cohort.Snapshot) || actual.SecretGeneration != w.plan.cohort.SecretState.Generation {
		return sandboxReceiptError()
	}
	if err := w.plan.authorise(ctx); err != nil {
		return err
	}
	if err := w.source.check(ctx, c); err != nil {
		return err
	}
	header, err := json.Marshal(proto.Request{ID: req.ID, Op: req.Op, Args: json.RawMessage("{}")})
	if err != nil || len(req.Args)+len(header)-2+1 > proto.MaxFrame {
		return &UserError{Msg: "the complete member request exceeds the protocol frame", Hint: "Select fewer local grants for this new member.", Code: "state_too_large", Kind: KindRefused}
	}
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	tier := min(tiers.Of(c.Host.Name), w.creation.Ceiling, requestCeiling(ctx, tiers.Of(c.Host.Name)))
	if err := recordSecretNames(ctx, c.Host, c.HostID(), tier, actual.Secrets); err != nil {
		return err
	}
	w.requestID, w.wire, w.prepared = req.ID, sha256.Sum256(req.Args), true
	return nil
}

func (w *freshChildPublicationWitness) wroteRequest(c *Conn, id uint64) {
	if w.prepared && c == w.conn && id == w.requestID {
		w.sent = true
	}
}

func (w *freshChildPublicationWitness) afterResponse(ctx context.Context, c *Conn, response proto.Response, out any) error {
	if !w.prepared || !w.sent || w.acknowledged || c != w.conn || !response.OK || response.ID != w.requestID {
		return sandboxReceiptError()
	}
	child, ok := out.(*Session)
	if !ok || child == nil || child.ID != w.creation.SessionID || child.Agent != w.plan.selection.Kind() ||
		child.SandboxOf != w.plan.ancestors.root.SessionID || child.RunGeneration != w.plan.cohort.RunGeneration ||
		!ValidHostID(child.RunGeneration) || child.SecretState == nil {
		return sandboxReceiptError()
	}
	if err := egress.ValidateAcknowledgedSessionSecretState(ctx, w.plan.selection.Kind(), w.plan.intent, w.args.Secrets, child.SecretState); err != nil {
		return err
	}
	if !reflect.DeepEqual(child.SecretState.Intent, w.plan.intent) {
		return sandboxReceiptError()
	}
	allowed := append([]string{}, w.plan.selection.names...)
	domain := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		domain[name] = true
	}
	state, err := egress.CloneLocalSessionSecretState(child.SecretState, domain)
	if err != nil {
		return err
	}
	if err := w.checkAppliedCohort(w.base, child); err != nil {
		return err
	}
	supplied, missing := append([]string{}, secretMapNames(w.args.Secrets)...), []string{}
	for _, name := range allowed {
		if !slices.Contains(supplied, name) {
			missing = append(missing, name)
		}
	}
	names, err := canonicalGrantNames(child.Secrets)
	if err != nil || !slices.Equal(names, allowed) {
		return sandboxReceiptError()
	}
	absent, err := canonicalGrantNames(child.MissingSecrets)
	if err != nil || !slices.Equal(absent, missing) {
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
	receipt := &sandboxDeliveryReceipt{Version: 1, Revision: revision, Creation: w.creation.request, Publication: publication, Operation: "spawn",
		Scope: sandboxReceiptScope{Host: c.Host.Name, Source: w.source.clone(), HostID: c.HostID(), MemberID: child.ID,
			RootID: w.plan.ancestors.root.SessionID, RunGeneration: child.RunGeneration, BackendGeneration: state.Generation},
		Allowed: allowed, Supplied: supplied, Missing: missing, State: state}
	w.receipt, w.acknowledged = cloneSandboxDeliveryReceipt(receipt), true
	if err := storeSandboxDeliveryReceiptContext(w.base, c, w.creation, "", receipt, w); err != nil {
		return err
	}
	w.plan.mu.Lock()
	w.plan.published = true
	w.plan.mu.Unlock()
	return nil
}

// A post-ACK read only restricts the explicit matching publication; it cannot
// fill in a missing ACK. It confirms the same run/backend and actual member,
// and checks ALL current cohort stand-ins against ALL freshly supplied values.
func (w *freshChildPublicationWitness) checkAppliedCohort(ctx context.Context, child *Session) error {
	current, err := w.conn.readFreshChildCohort(ctx, w.args.Parent)
	if err != nil {
		return err
	}
	if current.Snapshot.Primary != child.SandboxOf || current.Snapshot.Check(child.ID, proto.MaxFrame) != nil ||
		current.RunGeneration != child.RunGeneration || current.SecretState.Generation != child.SecretState.Generation {
		return sandboxReceiptError()
	}
	if err := egress.ValidateFreshValuesAgainstPublicState(ctx, current.SecretState, w.args.Secrets); err != nil {
		return err
	}
	for _, name := range w.plan.selection.names {
		if current.SecretState.Intent.Modes[name] != w.plan.intent.Modes[name] ||
			!reflect.DeepEqual(current.SecretState.Intent.Binds[name], w.plan.intent.Binds[name]) ||
			!reflect.DeepEqual(current.SecretState.Primary[name], child.SecretState.Primary[name]) ||
			!reflect.DeepEqual(current.SecretState.Minted[name], child.SecretState.Minted[name]) ||
			!reflect.DeepEqual(current.SecretState.Retired[name], child.SecretState.Retired[name]) {
			return sandboxReceiptError()
		}
	}
	return nil
}

func (w *freshChildPublicationWitness) checkSandboxReceiptWrite(ctx context.Context, intent *sandboxReceiptWriteIntent) error {
	if intent == nil || intent.kind != deliveryPublicationReceipt || intent.conn != w.conn || !w.prepared || !w.sent || !w.acknowledged ||
		w.requestID == 0 || w.receipt == nil || intent.expectedRevision != "" || !sameSandboxCreation(intent.original, w.creation) ||
		!reflect.DeepEqual(intent.delivery, w.receipt) {
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
