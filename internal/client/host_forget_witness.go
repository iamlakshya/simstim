package client

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
)

// HostForgetReceiptHooks bind preparation and pure verification to the owner's
// exact original host, captured identity, pin and completed removal attempt.
// Prepare changes only the existing receipt. Verify returns its target nonce.
type HostForgetReceiptHooks struct {
	Inspect HostStateReceiptInspector
	Prepare func(context.Context, string) error
	Verify  func(context.Context, []byte) (HostForgetMembershipProof, error)
}

// HostForgetMembershipProof is only owner-validated receipt data. The backend
// still requires exact current generation and saved-name absence for authority.
type HostForgetMembershipProof struct {
	FromGeneration   string
	TargetGeneration string
}

type hostForgetIntentKey struct{}
type hostForgetPreparationKey struct{}
type hostForgetIntent struct {
	original Host
	hostID   string
	hooks    HostForgetReceiptHooks
}
type hostForgetPreparation struct {
	intent           *hostForgetIntent
	generation       string
	sourceGeneration string
	active           atomic.Bool
}
type hostForgetWitness struct {
	intent             *hostForgetIntent
	original           Host
	hostID, generation string
}

// All saved-host snapshots share the canonical complete metadata clone.
func cloneForgetHost(h Host) Host { return cloneGrantHost(h) }

// WithHostForgetIntent binds only local preparation. It claims no completion.
// Bind immediately before the local CAS, after prompts and remote operations.
// Cancel releases the single cooperative budget for this local phase.
func WithHostForgetIntent(ctx context.Context, original Host, hostID string, hooks HostForgetReceiptHooks) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	held, _ := ctx.Value(heldKey{original.Name}).(*heldLock)
	if !ValidHostName(original.Name) || !ValidHostID(hostID) || held == nil || held.gone.Load() || hooks.Prepare == nil || hooks.Verify == nil {
		return ctx, nil, hostStateRemovalError("the original removal intent has no complete owner proof")
	}
	if ctx.Value(hostForgetMembershipKey{}) != nil {
		return ctx, nil, hostStateRemovalError("host membership cannot change during verified local cleanup")
	}
	budget, cancel := context.WithTimeout(ctx, hostsWait)
	intent := &hostForgetIntent{original: cloneForgetHost(original), hostID: hostID, hooks: hooks}
	return context.WithValue(budget, hostForgetIntentKey{}, intent), cancel, nil
}

// The owner also checks this private phase before changing a prepared target.
// No public boolean or serialized field can create this preparation scope.
func requireHostForgetPreparation(ctx context.Context, original Host, hostID, generation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, _ := ctx.Value(hostForgetPreparationKey{}).(*hostForgetPreparation)
	if p == nil || !p.active.Load() || p.generation != generation || p.intent.hostID != hostID || !SameEntry(p.intent.original, original) {
		return hostStateRemovalError("the original forget preparation is not active")
	}
	held, _ := ctx.Value(heldKey{original.Name}).(*heldLock)
	if held == nil || held.gone.Load() {
		return hostStateRemovalError("the original host operation lease ended")
	}
	return nil
}

// ApplyHostChange must already have compared the actual original while the
// canonical membership lock is held. Preparation failure commits no forget.
func prepareHostForgetGeneration(ctx context.Context, c HostChange, hosts []Host) (string, string, error) {
	intent, _ := ctx.Value(hostForgetIntentKey{}).(*hostForgetIntent)
	if intent == nil || c.Now != nil {
		return "", "", nil
	}
	if c.Was == nil || !SameEntry(*c.Was, intent.original) {
		return "", "", hostStateRemovalError("the removal intent does not match the compared original host")
	}
	proof, err := readHostForgetReceipt(ctx, intent)
	if err != nil {
		return "", "", err
	}
	current, readErr := currentMembershipGeneration(ctx)
	var from, target string
	if proof.FromGeneration == "" && proof.TargetGeneration == "" {
		if readErr != nil {
			if !errors.Is(readErr, errMembershipGenerationMissing) {
				return "", "", readErr
			}
			current, err = newMembershipGeneration()
			if err != nil {
				return "", "", err
			}
			if err := ctx.Err(); err != nil {
				return "", "", err
			}
			if err := writeHostsSelectedGeneration(hosts, current); err != nil {
				return "", "", err
			}
			actual, err := currentMembershipGeneration(ctx)
			if err != nil || actual != current {
				return "", "", hostStateRemovalError("the initial host membership generation was not published")
			}
		}
		from = current
		for target == "" || target == from {
			if err := ctx.Err(); err != nil {
				return "", "", err
			}
			target, err = newMembershipGeneration()
			if err != nil {
				return "", "", err
			}
		}
	} else {
		if !validHostForgetPair(proof) || readErr != nil || current != proof.FromGeneration {
			return "", "", hostStateRemovalError("saved hosts changed after this removal was prepared")
		}
		from, target = proof.FromGeneration, proof.TargetGeneration
	}
	phase := &hostForgetPreparation{intent: intent, generation: target, sourceGeneration: from}
	phase.active.Store(true)
	defer phase.active.Store(false)
	preparing := context.WithValue(ctx, hostForgetPreparationKey{}, phase)
	preparing = context.WithValue(preparing, stateAuthorityKey{}, func() error {
		return requireHostForgetPreparation(context.WithValue(ctx, hostForgetPreparationKey{}, phase), intent.original, intent.hostID, target)
	})
	err = intent.hooks.Prepare(preparing, target)
	phase.active.Store(false)
	if stopped := ctx.Err(); stopped != nil {
		return "", "", stopped
	}
	if err != nil {
		return "", "", err
	}
	written, err := readHostForgetReceipt(ctx, intent)
	if err != nil {
		return "", "", err
	}
	if written.FromGeneration != from || written.TargetGeneration != target {
		return "", "", hostStateRemovalError("the prepared receipt does not match the selected membership proof")
	}
	return from, target, nil
}

func hostForgetPreparationSource(ctx context.Context, original Host, hostID, target string) (string, error) {
	if err := requireHostForgetPreparation(ctx, original, hostID, target); err != nil {
		return "", err
	}
	phase := ctx.Value(hostForgetPreparationKey{}).(*hostForgetPreparation)
	if !ValidHostID(phase.sourceGeneration) {
		return "", hostStateRemovalError("the prepared removal has no authoritative source generation")
	}
	return phase.sourceGeneration, nil
}

func validHostForgetPair(proof HostForgetMembershipProof) bool {
	return ValidHostID(proof.FromGeneration) && ValidHostID(proof.TargetGeneration) && proof.FromGeneration != proof.TargetGeneration
}

func currentMembershipGeneration(ctx context.Context) (string, error) {
	snapshot, err := openHostMembershipSnapshot(ctx)
	if err != nil {
		return "", err
	}
	defer snapshot.close()
	return snapshot.generation, nil
}

func readHostForgetReceipt(ctx context.Context, intent *hostForgetIntent) (HostForgetMembershipProof, error) {
	var proof HostForgetMembershipProof
	err := withStateTransaction(ctx, []string{"hosts", HostFileKey(intent.original.Name)}, "removal.json", false, true, func(tx *stateFileTxn) error {
		view := tx.view()
		if !view.Exists {
			return hostStateRemovalError("the original removal receipt is missing")
		}
		raw, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
		if err != nil {
			return err
		}
		proof, err = intent.hooks.Verify(ctx, raw)
		if stopped := ctx.Err(); stopped != nil {
			return stopped
		}
		if err != nil {
			return err
		}
		if (proof.FromGeneration != "" || proof.TargetGeneration != "") && !validHostForgetPair(proof) {
			return hostStateRemovalError("the original receipt has no complete source-to-target proof")
		}
		return nil
	})
	return proof, err
}

func recordHostForgetWitness(ctx context.Context, c HostChange, selected string) {
	held, _ := ctx.Value(heldKey{c.name()}).(*heldLock)
	if held == nil {
		return
	}
	clearHostForgetWitness(held)
	var next *hostForgetWitness
	if c.Now == nil && c.Was != nil {
		current, err := openHostMembershipSnapshot(context.WithoutCancel(ctx))
		if err == nil {
			intent, _ := ctx.Value(hostForgetIntentKey{}).(*hostForgetIntent)
			if selected != "" && current.generation != selected {
				current.close()
				return
			}
			next = &hostForgetWitness{original: cloneForgetHost(*c.Was), generation: current.generation, intent: intent}
			if intent != nil {
				next.hostID = intent.hostID
			}
			current.close()
		}
	}
	held.forgetMu.Lock()
	defer held.forgetMu.Unlock()
	held.forgotten = nil
	if !held.gone.Load() {
		held.forgotten = next
	}
}
func clearHostForgetWitness(held *heldLock) {
	held.forgetMu.Lock()
	held.forgotten = nil
	held.forgetMu.Unlock()
}

func hasHostForgetWitness(ctx context.Context, original Host, intent *hostForgetIntent, generation string) bool {
	held, _ := ctx.Value(heldKey{original.Name}).(*heldLock)
	if held == nil || held.gone.Load() {
		return false
	}
	held.forgetMu.Lock()
	defer held.forgetMu.Unlock()
	w := held.forgotten
	return w != nil && SameEntry(w.original, original) && w.generation == generation && (intent == nil || w.intent == intent && w.hostID == intent.hostID)
}
