package client

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

var sandboxDeliveryReceiptFields = []string{"version", "revision", "previous", "creation", "publication", "operation", "scope", "allowed_names", "supplied_names", "missing_names", "state", "arm_supplied_names", "trusted_arm_supplied_names"}
var sandboxInputOperationFields = []string{"version", "revision", "previous", "creation", "operation", "scope", "nonce", "digest", "summary_hash", "selection_hash"}
var sandboxReceiptScopeFields = []string{"host", "source", "host_id", "member_id", "root_id", "run_generation", "backend_generation"}

// sandboxReceiptSpans retain offsets in the caller's SAME locked snapshot.
// They are only grammar inputs. In particular, State/Allowed must go to the
// complete canonical P23 WithSources inspector before any FD semantic claim.
// Source likewise remains the one engine Source witness, never a native token.
type sandboxReceiptSpans struct {
	Scope, Source, State, Allowed api.JSONSpan
	Future                        bool
	Version                       int64
}

func projectSandboxReceiptSpans(ctx context.Context, p jsonValueProjection, span api.JSONSpan, schema int, delivery bool) (sandboxReceiptSpans, error) {
	var result sandboxReceiptSpans
	if span.End == 0 {
		return result, nil
	}
	if schema != 4 {
		return result, badSandboxStateContext(ctx)
	}
	known := sandboxInputOperationFields
	if delivery {
		known = sandboxDeliveryReceiptFields
	}
	var version api.JSONSpan
	deltaPresent := false
	err := p.Object(span, known, func(field api.JSONField) error {
		if field.Known < 0 {
			result.Future = true
			return nil
		}
		switch known[field.Known] {
		case "version":
			version = field.Value
		case "scope":
			result.Scope = field.Value
		case "allowed_names":
			result.Allowed = field.Value
		case "state":
			result.State = field.Value
		case "arm_supplied_names", "trusted_arm_supplied_names":
			deltaPresent = true
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	var encoded [19]byte
	n := 0
	err = p.WalkEncoded(version, func(part []byte) error {
		if len(part) > len(encoded)-n {
			return badSandboxStateContext(ctx)
		}
		n += copy(encoded[n:], part)
		return nil
	})
	if err != nil {
		return result, err
	}
	v, err := sandboxLineageVersion(encoded[:n])
	if err != nil {
		return result, err
	}
	result.Version = v
	if v > 1 && (!delivery || v > 3) {
		result.Future = true
		return result, ctx.Err()
	}
	if delivery && v == 1 && deltaPresent {
		result.Future = true // Preserve v1 unknown-extension semantics.
	}
	err = p.Object(result.Scope, sandboxReceiptScopeFields, func(field api.JSONField) error {
		if field.Known < 0 {
			result.Future = true
		} else if field.Known == 1 {
			result.Source = field.Value
		}
		return nil
	})
	if err == nil && (result.Source.End == 0 || delivery && (result.State.End == 0 || result.Allowed.End == 0)) {
		err = badSandboxStateContext(ctx)
	}
	return result, err
}

func decodeSandboxReceiptsContext(ctx context.Context, raw json.RawMessage, schema int, row *SandboxPolicy) error {
	p, err := api.NewJSONBytesProjection(ctx, raw)
	if err != nil {
		return err
	}
	root, err := p.ObjectRoot()
	if err != nil {
		return err
	}
	var receipt, operation api.JSONSpan
	err = p.Object(root, []string{"delivery_receipt", "input_operation"}, func(field api.JSONField) error {
		if field.Known == 0 {
			receipt = field.Value
		} else if field.Known == 1 {
			operation = field.Value
		}
		return nil
	})
	if err != nil {
		return err
	}
	if receipt.End != 0 {
		shape, err := projectSandboxReceiptSpans(ctx, p, receipt, schema, true)
		if err != nil {
			return err
		}
		row.futureDeliveryReceipt = shape.Future
		if shape.Version == 1 || shape.Version == 2 || shape.Version == 3 {
			var decoded sandboxDeliveryReceipt
			if err := decodeSandboxDeliveryReceiptVersion(ctx, row.deliveryReceiptRaw, shape.Version, &decoded); err != nil {
				return err
			}
			knownErr := validateSandboxDeliveryReceiptKnownData(ctx, &decoded)
			if knownErr != nil && !errors.Is(knownErr, errSandboxReceiptStateFuture) {
				return knownErr
			}
			if !decoded.Scope.matchesPolicy(row) || decoded.Creation != row.request || !slices.Equal(decoded.Allowed, localSandboxNames(row)) {
				return sandboxReceiptError()
			}
			if knownErr == nil && !sandboxReceiptMatchesDelivery(row, &decoded) {
				return sandboxReceiptError()
			}
			row.futureDeliveryReceipt = row.futureDeliveryReceipt || knownErr != nil || decoded.Scope.Source.validate() != nil
			if !row.futureDeliveryReceipt {
				row.deliveryReceipt = cloneSandboxDeliveryReceipt(&decoded)
			}
		}
	}
	if operation.End != 0 {
		shape, err := projectSandboxReceiptSpans(ctx, p, operation, schema, false)
		if err != nil {
			return err
		}
		row.futureInputOperation = shape.Future
		if shape.Version == 1 {
			var decoded sandboxInputOperation
			if err := decodeContextJSON(ctx, row.inputOperationRaw, &decoded); err != nil {
				return err
			}
			if err := validateSandboxInputOperationKnownData(ctx, &decoded); err != nil {
				return err
			}
			if !decoded.Scope.matchesPolicy(row) || decoded.Creation != row.request {
				return sandboxReceiptError()
			}
			row.futureInputOperation = row.futureInputOperation || decoded.Scope.Source.validate() != nil
			if !row.futureInputOperation {
				row.inputOperation = cloneSandboxInputOperation(&decoded)
			}
		}
	}
	return ctx.Err()
}

// Original is the row's canonical LOCAL allowed-name span (secrets or known
// lineage), not a host/cohort list. Both comparisons retain only cursor spans.
func validateSandboxReceiptAllowedSpans(ctx context.Context, p jsonValueProjection, receipt, original api.JSONSpan) error {
	a, err := deliveryNamesCursor(p, receipt)
	if err != nil {
		return err
	}
	b, err := deliveryNamesCursor(p, original)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		x, xok, err := a.next()
		if err != nil {
			return err
		}
		y, yok, err := b.next()
		if err != nil {
			return err
		}
		if xok != yok {
			return sandboxReceiptError()
		}
		if !xok {
			return nil
		}
		order, err := p.CompareStrings(x, y)
		if err != nil || order != 0 {
			return sandboxReceiptError()
		}
	}
}
