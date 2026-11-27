package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
)

// Only the canonical state inspector's unsupported-shape classification may
// produce this marker. Known semantic failures must remain failures.
var errSandboxReceiptStateFuture = errors.New("canonical receipt state is opaque future data")

type receiptRowProjection struct {
	id, hostID, request, names, wall, lineage, delivery api.JSONSpan
}

func projectAgentSandboxReceipts(ctx context.Context, p jsonValueProjection, rowSpan, rowKey api.JSONSpan, schema int, facts *agentSandboxPolicy, validateState func(state, allowed api.JSONSpan) error, checkNames func(original, receipt api.JSONSpan) error, withRelations sandboxReceiptRelationPhase) error {
	known := []string{"session_id", "host_id", "request", "secrets", "wall_intent", "lineage", "delivery_intent", "delivery_receipt", "input_operation"}
	var values [9]api.JSONSpan
	if err := p.Object(rowSpan, known, func(f api.JSONField) error {
		if f.Known >= 0 {
			values[f.Known] = f.Value
		}
		return nil
	}); err != nil {
		return err
	}
	row := receiptRowProjection{id: values[0], hostID: values[1], request: values[2], names: values[3], wall: values[4], lineage: values[5], delivery: values[6]}
	for index, span := range values[7:] {
		if span.End == 0 {
			continue
		}
		delivery := index == 0
		future, err := projectKnownSandboxReceipt(ctx, p, span, rowKey, schema, row, facts, delivery, validateState, checkNames, withRelations)
		if err != nil {
			return err
		}
		if delivery {
			facts.FutureReceipt = future
		} else {
			facts.FutureInput = future
		}
	}
	return ctx.Err()
}

func projectKnownSandboxReceipt(ctx context.Context, p jsonValueProjection, span, rowKey api.JSONSpan, schema int, row receiptRowProjection, facts *agentSandboxPolicy, delivery bool, validateState func(api.JSONSpan, api.JSONSpan) error, checkNames func(api.JSONSpan, api.JSONSpan) error, withRelations sandboxReceiptRelationPhase) (bool, error) {
	shape, err := projectSandboxReceiptSpans(ctx, p, span, schema, delivery)
	if err != nil || shape.Version > 1 && (!delivery || shape.Version > 3) {
		return shape.Future, err
	}
	known := sandboxInputOperationFields
	if delivery {
		known = sandboxDeliveryReceiptFields
	}
	var fields [13]api.JSONSpan
	if err := p.Object(span, known, func(f api.JSONField) error {
		if f.Known >= 0 {
			fields[f.Known] = f.Value
		}
		return nil
	}); err != nil {
		return false, err
	}
	// The ordinary typed decoder runs before future Source/state semantics.
	// Preserve that type boundary, including null string/array defaults.
	strings := []int{1, 2, 3, 4, 6, 7, 8, 9}
	if delivery {
		strings = []int{1, 2, 3, 4, 5}
	}
	for _, i := range strings {
		if _, err := receiptProjectionString(p, fields[i]); err != nil {
			return false, err
		}
	}
	if delivery {
		arrays := []int{7, 8, 9}
		if shape.Version == 2 || shape.Version == 3 {
			arrays = append(arrays, 11, 12)
		}
		for _, i := range arrays {
			if err := receiptProjectionNames(p, fields[i], false); err != nil {
				return false, err
			}
		}
	}
	if delivery && shape.Version == 3 {
		for _, span := range []api.JSONSpan{fields[11], fields[12]} {
			if span.End == 0 {
				continue
			}
			null, err := p.Literal(span, "null")
			if err != nil || !null {
				return false, sandboxReceiptError()
			}
		}
	}
	var scope [7]api.JSONSpan
	if err := p.Object(shape.Scope, sandboxReceiptScopeFields, func(f api.JSONField) error {
		if f.Known >= 0 {
			scope[f.Known] = f.Value
		}
		return nil
	}); err != nil {
		return false, err
	}
	for _, i := range []int{0, 2, 3, 4, 5, 6} {
		if _, err := receiptProjectionString(p, scope[i]); err != nil {
			return false, err
		}
	}
	sourceFuture, err := projectReceiptSource(p, scope[1])
	if err != nil {
		return false, err
	}
	stateFuture := false
	if delivery {
		if validateState == nil {
			return false, sandboxReceiptError()
		}
		if err := validateState(shape.State, shape.Allowed); err != nil {
			if !errors.Is(err, errSandboxReceiptStateFuture) {
				return false, err
			}
			stateFuture = true
		}
	}
	if err := projectReceiptHeader(p, fields, delivery, shape.Version); err != nil {
		return false, err
	}
	if err := projectReceiptScope(p, scope, row, rowKey, facts, delivery); err != nil {
		return false, err
	}
	equal, err := p.CompareStrings(fields[3], row.request)
	if err != nil {
		return false, err
	}
	if equal != 0 {
		return false, sandboxReceiptError()
	}
	if !delivery {
		for _, i := range []int{6, 7, 8, 9} {
			if err := receiptProjectionHex(p, fields[i], 64); err != nil {
				return false, err
			}
		}
		return shape.Future || sourceFuture, ctx.Err()
	}
	for _, i := range []int{7, 8, 9} {
		if err := receiptProjectionNames(p, fields[i], true); err != nil {
			return false, err
		}
	}
	original, err := deliveryAllowedSpan(p, row.names, row.lineage, row.lineage.End != 0 && !facts.FutureLineage)
	if err != nil {
		return false, err
	}
	if checkNames == nil {
		return false, sandboxReceiptError()
	}
	if err := checkNames(original, shape.Allowed); err != nil {
		return false, err
	}
	if stateFuture {
		return true, ctx.Err()
	}
	state, err := receiptStateSpans(p, shape.State)
	if err != nil {
		return false, err
	}
	if order, err := p.CompareStrings(state.generation, scope[6]); err != nil || order != 0 {
		if err != nil {
			return false, err
		}
		return false, sandboxReceiptError()
	}
	if withRelations == nil {
		return false, sandboxReceiptError()
	}
	plan, wallNone, err := makeReceiptRelationPlan(p, state, row, fields[8], fields[9])
	if err != nil {
		return false, err
	}
	if shape.Version == 2 {
		plan.Groups[receiptStatePrimary], plan.Groups[receiptStateTrusted] = state.primary, state.trusted
		for _, delta := range []api.JSONSpan{fields[11], fields[12]} {
			if err := receiptProjectionNames(p, delta, true); err != nil {
				return false, err
			}
		}
	}
	err = withRelations(plan, func(lookup sandboxReceiptRelationLookup) error {
		if err := checkIndexedReceiptRelations(p, state, row.delivery.End != 0, wallNone, fields[8], fields[9], shape.Allowed, lookup); err != nil {
			return err
		}
		if shape.Version == 2 {
			return checkIndexedArmReceiptDeltasV2(p, fields[11], fields[12], lookup)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return shape.Future || sourceFuture, ctx.Err()
}

func projectReceiptHeader(p jsonValueProjection, fields [13]api.JSONSpan, delivery bool, version int64) error {
	for _, i := range []int{1, 3} {
		if err := receiptProjectionHex(p, fields[i], 32); err != nil {
			return err
		}
	}
	previous, err := receiptProjectionString(p, fields[2])
	if err != nil {
		return err
	}
	if previous {
		if err := receiptProjectionHex(p, fields[2], 32); err != nil {
			return err
		}
		order, err := p.CompareStrings(fields[2], fields[1])
		if err != nil {
			return err
		}
		if order == 0 {
			return sandboxReceiptError()
		}
	}
	operation := fields[4]
	if delivery {
		operation = fields[5]
		if err := receiptProjectionHex(p, fields[4], 32); err != nil {
			return err
		}
	}
	if delivery && version == 2 {
		arm, err := p.StringEqual(operation, "arm")
		if err != nil || !arm {
			return sandboxReceiptError()
		}
		return nil
	}
	if delivery && version == 3 {
		rewind, err := p.StringEqual(operation, "rewind")
		if err != nil || !rewind {
			return sandboxReceiptError()
		}
		return nil
	}
	for _, kind := range []string{"new", "resume", "spawn"} {
		ok, err := p.StringEqual(operation, kind)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return sandboxReceiptError()
}

func projectReceiptScope(p jsonValueProjection, scope [7]api.JSONSpan, row receiptRowProjection, key api.JSONSpan, facts *agentSandboxPolicy, delivery bool) error {
	var host [63]byte
	n, err := projectedBoundedBytes(p, scope[0], host[:])
	if err != nil {
		return err
	}
	if !ValidHostName(string(host[:n])) {
		return sandboxReceiptError()
	}
	for _, i := range []int{2, 5} {
		if err := receiptProjectionHex(p, scope[i], 32); err != nil {
			return err
		}
	}
	backend, err := receiptProjectionString(p, scope[6])
	if err != nil {
		return err
	}
	if backend {
		if err := receiptProjectionHex(p, scope[6], 32); err != nil {
			return err
		}
	} else if delivery {
		return sandboxReceiptError()
	}
	for _, i := range []int{3, 4} {
		if err := projectedSessionID(p, scope[i]); err != nil {
			return err
		}
	}
	if facts.FutureLineage || facts.FutureDelivery {
		return sandboxReceiptError()
	}
	for _, pair := range [][2]api.JSONSpan{{scope[2], row.hostID}, {scope[3], key}} {
		order, err := p.CompareStrings(pair[0], pair[1])
		if err != nil {
			return err
		}
		if order != 0 {
			return sandboxReceiptError()
		}
	}
	root := row.id
	if row.lineage.End != 0 {
		if err := p.Object(row.lineage, []string{"root"}, func(f api.JSONField) error {
			if f.Known == 0 {
				root = f.Value
			}
			return nil
		}); err != nil {
			return err
		}
	}
	order, err := p.CompareStrings(scope[4], root)
	if err != nil {
		return err
	}
	if order != 0 {
		return sandboxReceiptError()
	}
	return nil
}
