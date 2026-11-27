package client

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

type freshSandboxReservationIntent struct {
	conn     *Conn
	snapshot *freshSandboxReservationSnapshot
	child    *SandboxPolicy
}

// Main's actual sealed fresh-local selection/request/current-cohort witness
// owns this lease. No row, remote name, public flag or metadata DTO implements
// it. It validates the SAME actual Conn, original ancestors/Source, selected
// kind/names/modes/binds/exposure and actual coherent current cohort outside
// byte callbacks; release keeps them immutable until the local writer joins.
type freshSandboxReservationProof interface {
	holdFreshSandboxReservation(context.Context, *freshSandboxReservationIntent) (cohort *api.SandboxCohortView, release func(), err error)
}

type freshSandboxReservationKey struct{}
type freshSandboxReservationPermit struct {
	intent freshSandboxReservationIntent
	cohort *api.SandboxCohortView
	active atomic.Bool
	used   atomic.Bool
}

// Call before any fresh value command. The ONE writer atomically installs the
// schema4 barrier and child row; parent/root bytes remain unchanged. Main must
// retain the actual saved child snapshot under this same original OP before
// commands, then use its actual protected same-cohort CAS at publication.
func reserveFreshSandboxContext(ctx context.Context, c *Conn, snapshot *freshSandboxReservationSnapshot, child SandboxPolicy, proof freshSandboxReservationProof) error {
	if proof == nil || ctx.Value(freshSandboxReservationKey{}) != nil {
		return joinIntentError()
	}
	frozen := cloneFreshSandboxSnapshot(snapshot)
	return withSandboxReceiptObservation(ctx, c, func(owned context.Context) error {
		if err := frozen.check(owned, c); err != nil {
			return err
		}
		normal, err := normaliseFreshSandboxChild(owned, frozen, child)
		if err != nil {
			return err
		}
		intent := freshSandboxReservationIntent{conn: c, snapshot: frozen, child: normal}
		cohort, release, err := proof.holdFreshSandboxReservation(owned, &intent)
		if err != nil {
			return err
		}
		if release == nil {
			return joinIntentError()
		}
		defer release()
		permit := &freshSandboxReservationPermit{intent: intent, cohort: cohort.Clone()}
		if err := validateFreshSandboxCohort(owned, &permit.intent, permit.cohort); err != nil {
			return err
		}
		permit.active.Store(true)
		bound, cancel := context.WithCancel(owned)
		bound = context.WithValue(bound, freshSandboxReservationKey{}, permit)
		revoke := sync.OnceFunc(func() { permit.active.Store(false); cancel() })
		defer revoke()
		if err := reserveSandbox(bound, c.Host, *normal); err != nil {
			return err
		}
		return frozen.check(owned, c)
	})
}

func normaliseFreshSandboxChild(ctx context.Context, s *freshSandboxReservationSnapshot, child SandboxPolicy) (*SandboxPolicy, error) {
	p := cloneSandboxPolicy(&child)
	var err error
	*p, err = normalizeCreationIntent(*p)
	if err != nil {
		return nil, err
	}
	if p.lineage == nil || p.lineage.Version != 2 || p.lineage.Parent != s.parent.SessionID || p.lineage.Root != s.root.SessionID ||
		p.HostID != s.parent.HostID || p.SSHAgent || p.FutureLineage || p.FutureDelivery || p.CreationWall != s.root.CreationWall ||
		!bytes.Equal(p.imageSelectionRaw, s.root.imageSelectionRaw) || p.Ceiling > s.parent.Ceiling || p.Ceiling > s.root.Ceiling || !bytes.Equal(p.PolicySources, s.root.PolicySources) ||
		!slices.Equal(p.Secrets, p.lineage.Allowed) || p.DeliveryIntent == nil || p.DeliveryIntent.Version != api.SecretDeliveryIntentV1 ||
		p.deliveryReceipt != nil || p.inputOperation != nil || p.deliveryReceiptRaw != nil || p.inputOperationRaw != nil || p.futureDeliveryReceipt || p.futureInputOperation {
		return nil, joinIntentError()
	}
	wall, err := json.Marshal(savedCreationIntent{Level: p.CreationWall, Exposed: p.Exposed})
	if err != nil {
		return nil, err
	}
	lineage, err := json.Marshal(p.lineage)
	if err != nil {
		return nil, err
	}
	delivery, err := json.Marshal(p.DeliveryIntent)
	if err != nil {
		return nil, err
	}
	if p.wallIntent != nil && !bytes.Equal(p.wallIntent, wall) || p.lineageRaw != nil && !bytes.Equal(p.lineageRaw, lineage) || p.deliveryRaw != nil && !bytes.Equal(p.deliveryRaw, delivery) {
		return nil, joinIntentError()
	}
	encoded, err := json.Marshal(savedSandbox{SessionID: p.SessionID, HostID: p.HostID, Ceiling: p.Ceiling.String(), SSHAgent: false,
		Secrets: p.Secrets, CreatedAt: p.CreatedAt, Request: p.request, WallIntent: wall, PolicySources: p.PolicySources, Lineage: lineage, DeliveryIntent: delivery, ImageSelection: p.imageSelectionRaw})
	if err != nil {
		return nil, err
	}
	// The policy/resource pin is opaque and byte-exact, including nested
	// unknown formatting. The standard encoder compacts RawMessage values;
	// restore this original pin through the SAME raw-value object writer.
	fields, err := sandboxObjectContext(ctx, encoded)
	if err != nil {
		return nil, err
	}
	fields["policy_sources"] = bytes.Clone(p.PolicySources)
	if p.imageSelectionRaw != nil {
		fields["image_selection_v1"] = bytes.Clone(p.imageSelectionRaw)
	}
	encoded, err = marshalSandboxObject(fields)
	if err != nil {
		return nil, err
	}
	// Reuse the sole saved-row codec, including canonical delivery bindings
	// and OWN exposure domain. No inherited parent wall_intent raw is copied.
	decoded, err := decodeSandboxContext(ctx, p.SessionID, encoded, 4)
	if err != nil || decoded.FutureLineage || decoded.FutureDelivery {
		if err != nil {
			return nil, err
		}
		return nil, joinIntentError()
	}
	return &decoded, nil
}

func validateFreshSandboxCohort(ctx context.Context, intent *freshSandboxReservationIntent, cohort *api.SandboxCohortView) error {
	if cohort == nil || cohort.Snapshot == nil || cohort.Snapshot.Check(intent.snapshot.parent.SessionID, proto.MaxFrame) != nil ||
		cohort.Snapshot.Primary != intent.snapshot.root.SessionID || !ValidHostID(cohort.RunGeneration) {
		return joinIntentError()
	}
	if err := egress.ValidateSavedPublicSessionState(cohort.SecretState); err != nil {
		return err
	}
	return checkFreshCohortDelivery(ctx, cohort.SecretState.Intent, *intent.child.DeliveryIntent)
}

// Existing unit-wide names restrict the new LOCAL tuple. No remote key can
// select a name or turn an old bound binding into exposure/a wider route.
func checkFreshCohortDelivery(ctx context.Context, current, proposed api.SecretDeliveryIntent) error {
	for name, mode := range proposed.Modes {
		if err := ctx.Err(); err != nil {
			return err
		}
		existing, overlap := current.Modes[name]
		if !overlap {
			continue // Cohort restricts; it supplies no new local name authority.
		}
		if existing != mode {
			return joinIntentError()
		}
		if mode == api.SecretModeBound {
			a, err := secretbind.Canonical(current.Binds[name])
			if err != nil {
				return err
			}
			b, err := secretbind.Canonical(proposed.Binds[name])
			if err != nil || !reflect.DeepEqual(a, b) {
				return joinIntentError()
			}
		}
	}
	return nil
}
