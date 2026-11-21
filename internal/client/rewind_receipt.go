package client

import (
	"context"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func (w *rewindPublicationWitnessV3) prepareReceiptsV3(ctx context.Context) error {
	w.mu.Lock()
	if w.receipts != nil {
		w.mu.Unlock()
		return nil
	}
	cohort := w.cohort.Clone()
	w.mu.Unlock()
	if cohort == nil || !w.hasAcknowledgementV3() {
		return sandboxReceiptError()
	}
	w.plan.mu.Lock()
	group := cloneResumeGrantIntent(w.plan.group)
	w.plan.mu.Unlock()
	receipts := map[string]*sandboxDeliveryReceipt{}
	for _, id := range append([]string{group.snapshot.Primary}, group.snapshot.Members...) {
		original := group.members[id].creation
		if original == nil {
			continue
		} // A remote member cannot create a laptop grant domain.
		allowed := append([]string{}, localSandboxNames(original)...)
		domain := make(map[string]bool, len(allowed))
		for _, name := range allowed {
			domain[name] = true
		}
		state, err := egress.ProjectLocalSessionSecretStateV1(cohort.SecretState, domain)
		if err != nil {
			return err
		}
		intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}, TrustedUses: api.CloneTrustedSecretUses(state.Intent.TrustedUses)}
		for name, mode := range state.Intent.Modes {
			if original.DeliveryIntent != nil {
				if original.DeliveryIntent.Modes[name] != mode {
					return sandboxReceiptError()
				}
				if mode == api.SecretModeBound && !reflect.DeepEqual(original.DeliveryIntent.Binds[name], state.Intent.Binds[name]) {
					return sandboxReceiptError()
				}
			} else if mode != api.SecretModeExposed || original.CreationWall != "none" && !slices.Contains(original.Exposed, name) {
				return sandboxReceiptError()
			}
			intent.Modes[name] = mode
			if mode == api.SecretModeBound {
				intent.Binds[name] = state.Intent.Binds[name]
			}
		}
		w.plan.mu.Lock()
		values := w.plan.values[id]
		err = egress.ValidateAcknowledgedSessionSecretState(ctx, "", intent, values, state)
		supplied := append([]string{}, secretMapNames(values)...)
		w.plan.mu.Unlock()
		if err != nil {
			return err
		}
		missing := []string{}
		for _, name := range allowed {
			if _, declared := state.Intent.Modes[name]; declared && !slices.Contains(supplied, name) {
				missing = append(missing, name)
			}
		}
		previous := ""
		if original.deliveryReceipt != nil {
			previous = original.deliveryReceipt.Revision
		}
		revision, err := newLocalReceiptID()
		if err != nil {
			return err
		}
		publication, err := newLocalReceiptID()
		if err != nil {
			return err
		}
		receipt := &sandboxDeliveryReceipt{Version: 3, Revision: revision, Previous: previous, Creation: original.request, Publication: publication, Operation: "rewind",
			Scope:   sandboxReceiptScope{Host: w.conn.Host.Name, Source: w.source.clone(), HostID: w.conn.HostID(), MemberID: id, RootID: group.snapshot.Primary, RunGeneration: cohort.RunGeneration, BackendGeneration: state.Generation},
			Allowed: allowed, Supplied: supplied, Missing: missing, State: state}
		if err := validateSandboxDeliveryReceipt(ctx, receipt); err != nil {
			return err
		}
		receipts[id] = receipt
	}
	w.mu.Lock()
	w.receipts = receipts
	w.mu.Unlock()
	return nil
}

// This proof belongs only to the actual final rewind frame and its correlated
// ACK. Public Source/generation/receipt data cannot implement this method.
func (w *rewindPublicationWitnessV3) checkSandboxReceiptWrite(ctx context.Context, intent *sandboxReceiptWriteIntent) error {
	if w == nil || intent == nil || intent.original == nil || intent.kind != rewindPublicationReceipt || intent.conn != w.conn || originalRewindV1(ctx) != w.plan || !w.hasAcknowledgementV3() {
		return sandboxReceiptError()
	}
	w.mu.Lock()
	receipt := cloneSandboxDeliveryReceipt(w.receipts[intent.original.SessionID])
	cohort := w.cohort.Clone()
	w.mu.Unlock()
	w.plan.mu.Lock()
	current := w.plan.publication == w
	original := cloneSandboxPolicy(w.plan.group.members[intent.original.SessionID].creation)
	w.plan.mu.Unlock()
	if !current || receipt == nil || cohort == nil || receipt.Scope.RunGeneration != cohort.RunGeneration || receipt.Scope.BackendGeneration != cohort.SecretState.Generation || !sameSandboxCreation(original, intent.original) || intent.expectedRevision != receipt.Previous || !reflect.DeepEqual(intent.delivery, receipt) {
		return sandboxReceiptError()
	}
	if err := w.plan.checkLocal(ctx); err != nil {
		return err
	}
	return w.source.check(ctx, w.conn)
}

// One existing native file transaction publishes every original local member.
// Failure before commit changes no row; a byte-identical actual ACK may retry.
func storeRewindDeliveryReceiptsV3(ctx context.Context, c *Conn, group *resumeGrantIntent, receipts map[string]*sandboxDeliveryReceipt, proof sandboxReceiptWriteProof) error {
	if proof == nil || c == nil || group == nil || group.snapshot == nil {
		return sandboxReceiptError()
	}
	intents := []sandboxReceiptWriteIntent{}
	for _, id := range append([]string{group.snapshot.Primary}, group.snapshot.Members...) {
		original := group.members[id].creation
		if original == nil {
			if receipts[id] != nil {
				return sandboxReceiptError()
			}
			continue
		}
		receipt := receipts[id]
		if receipt == nil || receipt.Version != 3 || receipt.Operation != "rewind" {
			return sandboxReceiptError()
		}
		intents = append(intents, sandboxReceiptWriteIntent{kind: rewindPublicationReceipt, conn: c, original: cloneSandboxPolicy(original), expectedRevision: receipt.Previous, delivery: cloneSandboxDeliveryReceipt(receipt)})
	}
	if len(intents) != len(receipts) {
		return sandboxReceiptError()
	}
	if len(intents) == 0 {
		return ctx.Err()
	}
	return withSandboxReceiptObservation(ctx, c, func(owned context.Context) error {
		for index := range intents {
			if err := validateSandboxReceiptWriteIntent(owned, &intents[index]); err != nil {
				return err
			}
			if err := proof.checkSandboxReceiptWrite(owned, &intents[index]); err != nil {
				return err
			}
		}
		if err := withSandboxFile(owned, c.Host, true, func(file *sandboxFile) error {
			changed := false
			for index := range intents {
				err := changeSandboxReceiptRow(owned, file, &intents[index])
				if err == errStateNoChange {
					continue
				}
				if err != nil {
					return err
				}
				changed = true
			}
			if !changed {
				return errStateNoChange
			}
			return nil
		}); err != nil {
			return err
		}
		for index := range intents {
			if err := validateSandboxReceiptWriteIntent(owned, &intents[index]); err != nil {
				return err
			}
			if err := proof.checkSandboxReceiptWrite(owned, &intents[index]); err != nil {
				return err
			}
		}
		return nil
	})
}
