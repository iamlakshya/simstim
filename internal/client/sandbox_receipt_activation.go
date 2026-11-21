package client

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// MarshalJSON preserves the v1 representation. Only v2 emits its two explicit
// delta arrays, including present-empty arrays for guest-only or trusted-only
// operations. No credential value or private wire hash enters this object.
func (r sandboxDeliveryReceipt) MarshalJSON() ([]byte, error) {
	type plain sandboxDeliveryReceipt
	value := plain(r)
	if r.Version != 2 {
		return json.Marshal(value)
	}
	return json.Marshal(struct {
		*plain
		ArmSupplied        []string `json:"arm_supplied_names"`
		TrustedArmSupplied []string `json:"trusted_arm_supplied_names"`
	}{&value, r.ArmSupplied, r.TrustedArmSupplied})
}

func decodeSandboxDeliveryReceiptVersion(ctx context.Context, raw json.RawMessage, version int64, out *sandboxDeliveryReceipt) error {
	if version != 1 {
		return decodeContextJSON(ctx, raw, out)
	}
	// In v1 these names were unknown extensions. Shadow only their new fields
	// while decoding known v1 data, so even an opaque extension's scalar keeps
	// the original future-preservation/type-priority behaviour.
	legacy := struct {
		*sandboxDeliveryReceipt
		ArmSupplied        json.RawMessage `json:"arm_supplied_names"`
		TrustedArmSupplied json.RawMessage `json:"trusted_arm_supplied_names"`
	}{sandboxDeliveryReceipt: out}
	return decodeContextJSON(ctx, raw, &legacy)
}

func validateSandboxArmReceiptDataV2(ctx context.Context, r *sandboxDeliveryReceipt) error {
	if r.Version == 3 {
		if r.Operation != "rewind" || r.ArmSupplied != nil || r.TrustedArmSupplied != nil {
			return sandboxReceiptError()
		}
		return ctx.Err()
	}
	if r.Version == 1 {
		if r.ArmSupplied != nil || r.TrustedArmSupplied != nil {
			return sandboxReceiptError()
		}
		return ctx.Err()
	}
	if r.Version != 2 || r.Operation != "arm" || len(r.ArmSupplied)+len(r.TrustedArmSupplied) == 0 {
		return sandboxReceiptError()
	}
	for _, names := range [][]string{r.ArmSupplied, r.TrustedArmSupplied} {
		if err := validateSandboxReceiptNames(ctx, names); err != nil {
			return err
		}
	}
	for _, name := range r.ArmSupplied {
		provided, present := r.State.Primary[name]
		_, supplied := slices.BinarySearch(r.Supplied, name)
		if !present || !supplied || r.State.Intent.Modes[name] != api.SecretModeBound || provided.Mode != api.SecretModeBound || !provided.Armed {
			return sandboxReceiptError()
		}
	}
	for _, name := range r.TrustedArmSupplied {
		if _, present := r.State.Intent.TrustedUses[name]; !present {
			return sandboxReceiptError()
		}
	}
	return ctx.Err()
}

// This lower writer still requires the actual original private arm capsule's
// existing write/ACK proof. A public state, generation or receipt DTO cannot
// advance it. The native file writer compares the exact previous revision.
func storeArmDeliveryReceiptV2(ctx context.Context, c *Conn, original *SandboxPolicy, expectedRevision string, next *sandboxDeliveryReceipt, proof sandboxReceiptWriteProof) error {
	if next == nil || next.Version != 2 || next.Operation != "arm" {
		return sandboxReceiptError()
	}
	return writeSandboxReceiptContext(ctx, sandboxReceiptWriteIntent{kind: armPublicationReceipt, conn: c,
		original: cloneSandboxPolicy(original), expectedRevision: expectedRevision, delivery: cloneSandboxDeliveryReceipt(next)}, proof)
}

func checkIndexedArmReceiptDeltasV2(p jsonValueProjection, guest, trusted api.JSONSpan, lookup sandboxReceiptRelationLookup) error {
	var count uint64
	if err := p.Array(guest, func(name api.JSONSpan) error {
		count++
		_, supplied, err := lookup(receiptSupplied, name)
		if err != nil {
			return err
		}
		primary, present, err := lookup(receiptStatePrimary, name)
		if err != nil {
			return err
		}
		if !present || !supplied {
			return sandboxReceiptError()
		}
		var mode, armed api.JSONSpan
		if err := p.Object(primary, []string{"mode", "armed"}, func(field api.JSONField) error {
			if field.Known == 0 {
				mode = field.Value
			} else if field.Known == 1 {
				armed = field.Value
			}
			return nil
		}); err != nil {
			return err
		}
		bound, err := p.StringEqual(mode, string(api.SecretModeBound))
		if err != nil {
			return err
		}
		yes, err := p.Literal(armed, "true")
		if err != nil {
			return err
		}
		if !bound || !yes {
			return sandboxReceiptError()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := p.Array(trusted, func(name api.JSONSpan) error {
		count++
		_, found, err := lookup(receiptStateTrusted, name)
		if err != nil {
			return err
		}
		if !found {
			return sandboxReceiptError()
		}
		return nil
	}); err != nil {
		return err
	}
	if count == 0 {
		return sandboxReceiptError()
	}
	return nil
}
