package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func inspectAgentSandboxPolicies(h *tomlinspect.Inspection, host, session string) (*agentSandboxPolicy, error) {
	ctx, err := h.Context()
	if err != nil {
		return nil, err
	}
	source, err := h.Source(1)
	if err != nil {
		return nil, err
	}
	if source.Kind() != tomlinspect.MetadataSandboxJSON {
		return nil, tomlinspect.ErrIndexInvariant
	}
	p, err := api.NewJSONProjection(ctx, source, source.Size())
	if err != nil {
		return nil, agentSandboxInspectionFailure(ctx, err)
	}
	if !p.MatchesSource(ctx, source, source.Size()) {
		return nil, tomlinspect.ErrIndexInvariant
	}
	// The schema is derived once, inside this complete inspection. Neither
	// counting nor the projection's source identity grants semantic support.
	schema := egress.NewPublicStateProjectionSchema()
	validateState := func(state, allowed api.JSONSpan) error {
		for _, span := range [...]api.JSONSpan{state, allowed} {
			if err := h.CheckSpan(1, tomlinspect.Span{Start: span.Start, End: span.End}); err != nil {
				return err
			}
		}
		err := schema.Validate(ctx, p, state, allowed, h)
		if errors.Is(err, egress.ErrUnsupportedPublicState) {
			// The canonical complete DTO classification emits this ONLY
			// before any failing count/region call can poison the inspection.
			return errSandboxReceiptStateFuture
		}
		return err
	}
	var receiptFailure error
	selected, err := projectAgentSandboxPolicyProjection(ctx, p, host, session, func(row, key api.JSONSpan, version int, facts *agentSandboxPolicy) error {
		checkNames := func(original, receipt api.JSONSpan) error {
			return checkAgentReceiptNames(ctx, p, row, original, receipt, h)
		}
		withRelations := func(plan sandboxReceiptRelationPlan, check func(sandboxReceiptRelationLookup) error) error {
			return withAgentReceiptRelations(ctx, p, row, plan, h, check)
		}
		receiptFailure = projectAgentSandboxReceipts(ctx, p, row, key, version, facts, validateState, checkNames, withRelations)
		return receiptFailure
	})
	if receiptFailure != nil {
		return nil, agentSandboxInspectionFailure(ctx, receiptFailure)
	}
	if err != nil {
		return nil, agentSandboxInspectionFailure(ctx, err)
	}
	return selected, nil
}

func agentSandboxInspectionFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, tomlinspect.ErrIndexIO) || errors.Is(err, tomlinspect.ErrIndexCorrupt) || errors.Is(err, tomlinspect.ErrIndexInvariant) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	return badSandboxState()
}
