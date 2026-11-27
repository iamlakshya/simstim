package client

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// Only this actual combined source producer returns these reduced facts.
// Historical Source is compared with the original checked Conn; no stored
// Source DTO supplies authority or opens a replacement settings operation.
type localForgeAckFacts struct {
	scope          sandboxReceiptScope
	revision       string
	creation       string
	ceiling        Tier
	name           string // borrowed from the canonical caller candidate set
	stateReference cachefs.Reference
}

type localForgeAckSelection struct {
	facts      localForgeAckFacts
	candidates map[string]bool
}

func (c *Conn) localForgeAckFacts(ctx context.Context, operation *settingsOperation, mode *agentModeResources, id string, candidates map[string]bool) (localForgeAckFacts, error) {
	var selected localForgeAckSelection
	if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err != nil {
		return selected.facts, err
	}
	if c == nil || c.agentOnly || operation == nil || mode == nil || mode.inspector == nil || mode.inspector.engine == nil {
		return selected.facts, grantPurposeError()
	}
	if err := checkSessionID(c.Host.Name, id); err != nil {
		return selected.facts, err
	}
	snapshot, err := operation.Snapshot(ctx)
	if err != nil {
		return selected.facts, err
	}
	selected.candidates = candidates
	err = withSandboxReceiptObservation(ctx, c, func(owned context.Context) error {
		source, err := captureHostStateSourceWitness(owned, c)
		if err != nil {
			return err
		}
		selected.facts.scope = sandboxReceiptScope{Host: c.Host.Name, Source: source, HostID: c.HostID(), MemberID: id}
		return withStateTransaction(owned, []string{"hosts", HostFileKey(c.Host.Name)}, "sandboxes.json", false, false, func(tx *stateFileTxn) error {
			if tx.source == nil {
				return sandboxReceiptError()
			}
			scope, err := newAgentMetadataScope(mode, snapshot, tx)
			if err != nil {
				return err
			}
			defer scope.close()
			err = mode.inspector.engine.WithSources(scope.ctx, scope.descriptors(), func(h *tomlinspect.Inspection) error {
				defer scope.finishCachedWork()
				if err := h.InspectTOML(0, &mode.inspector.visitor); err != nil {
					return err
				}
				_, err := inspectAgentSandboxPoliciesForForge(h, c.Host.Name, id, &selected)
				return err
			})
			if err != nil {
				return agentSettingsFailure(err)
			}
			if selected.facts.revision == "" {
				return sandboxReceiptError()
			}
			if err := scope.check(scope.ctx); err != nil {
				return err
			}
			selected.facts.stateReference, err = cachefs.CaptureFile(tx.root, tx.name, tx.source, 0600)
			if err != nil {
				return err
			}
			return tx.check()
		})
	})
	if err == nil {
		err = operation.Revalidate(ctx)
	}
	if err != nil {
		return localForgeAckFacts{}, err
	}
	return selected.facts, nil
}

func (s *localForgeAckSelection) capture(p jsonValueProjection, row api.JSONSpan, policy *agentSandboxPolicy) error {
	if policy == nil || policy.FutureLineage || policy.FutureDelivery || policy.FutureReceipt || policy.FutureInput {
		return futureSandboxReceiptError()
	}
	if policy.HostID != s.facts.scope.HostID {
		return sandboxReceiptError()
	}
	var receipt api.JSONSpan
	if err := p.Object(row, []string{"delivery_receipt"}, func(f api.JSONField) error {
		if f.Known == 0 {
			receipt = f.Value
		}
		return nil
	}); err != nil {
		return err
	}
	if receipt.End == 0 {
		return sandboxReceiptError()
	}
	var fields [11]api.JSONSpan
	if err := p.Object(receipt, sandboxDeliveryReceiptFields, func(f api.JSONField) error {
		if f.Known >= 0 {
			fields[f.Known] = f.Value
		}
		return nil
	}); err != nil {
		return err
	}
	var err error
	s.facts.revision, err = forgeAckFixedString(p, fields[1], 32)
	if err != nil {
		return err
	}
	s.facts.creation, err = forgeAckFixedString(p, fields[3], 32)
	if err != nil {
		return err
	}
	if err := s.captureScope(p, fields[6]); err != nil {
		return err
	}
	s.facts.ceiling = policy.Ceiling
	if len(s.candidates) == 0 {
		return nil
	}
	// Only canonical local candidate pointers are sorted. No history name,
	// state, binding or unselected row is decoded into another collection.
	candidates := slices.Sorted(maps.Keys(s.candidates))
	names, err := p.OrderedStrings(fields[7])
	if err != nil {
		return err
	}
	candidate := 0
	for candidate < len(candidates) {
		span, more, err := names.Next()
		if err != nil || !more {
			return err
		}
		for candidate < len(candidates) {
			name := candidates[candidate]
			order, err := compareForgeAckName(p, span, name)
			if err != nil {
				return err
			}
			if order == 0 && s.candidates[name] {
				s.facts.name = name
				return nil
			}
			if order < 0 {
				break
			}
			candidate++
		}
	}
	return nil
}

func forgeAckFixedString(p jsonValueProjection, span api.JSONSpan, maximum int) (string, error) {
	var storage [63]byte
	if maximum > len(storage) {
		return "", sandboxReceiptError()
	}
	n, err := projectedBoundedBytes(p, span, storage[:maximum])
	if err != nil {
		return "", err
	}
	return string(storage[:n]), nil
}

func compareForgeAckName(p jsonValueProjection, span api.JSONSpan, name string) (int, error) {
	index, order := 0, 0
	err := p.WalkString(span, func(r rune) error {
		if order == 0 {
			switch {
			case index == len(name) || r > rune(name[index]):
				order = 1
			case r < rune(name[index]):
				order = -1
			}
		}
		index++
		return nil
	})
	if order == 0 && index < len(name) {
		order = -1
	}
	return order, err
}
