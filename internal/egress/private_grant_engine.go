package egress

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

func copyGrantEngine(engine *Engine) *Engine {
	result := *engine
	result.layers = slices.Clone(engine.layers)
	for index := range result.layers {
		result.layers[index].rules = slices.Clone(result.layers[index].rules)
	}
	return &result
}

// Consumed once rows no longer spend private state capacity. Their immutable
// engine is replaced as usual; readers never observe an in-place slice edit.
func liveMemoryRows(engine *Engine) []compiledRule {
	engine.ledger.mu.Lock()
	defer engine.ledger.mu.Unlock()
	var rows []compiledRule
	for _, layer := range engine.layers {
		for _, row := range layer.rules {
			if row.memoryOnly && (!row.once || row.slot != nil && row.slot.state != slotConsumed) {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func copyLiveGrantEngine(engine *Engine) *Engine {
	next := copyGrantEngine(engine)
	engine.ledger.mu.Lock()
	defer engine.ledger.mu.Unlock()
	for index := range next.layers {
		next.layers[index].rules = slices.DeleteFunc(next.layers[index].rules, func(row compiledRule) bool {
			return row.memoryOnly && row.once && (row.slot == nil || row.slot.state == slotConsumed)
		})
	}
	return next
}

func appendMemoryRows(engine *Engine, rows []compiledRule) {
	for index, layer := range engine.layers {
		if layer.kind == api.LayerSession && layer.name == "" && layer.source == "" {
			engine.layers[index].rules = append(layer.rules, rows...)
			return
		}
	}
	index := 0
	for index < len(engine.layers) && engine.layers[index].kind == api.LayerLockdown {
		index++
	}
	engine.layers = slices.Insert(engine.layers, index, compiledLayer{kind: api.LayerSession, rules: slices.Clone(rows)})
}

// All fallible carrying and charging precedes existing registry installation.
// Cold initialization calls no carry; stopped/new actors cannot inherit rows.
func (s *Server) prepareMemoryCarryLocked(ctx context.Context, next *preparedInitial) error {
	current := s.currentEngine()
	rows := liveMemoryRows(current)
	if len(rows) == 0 {
		return nil
	}
	engine := copyGrantEngine(next.engine)
	publicIDs := make(map[string]struct{})
	for _, layer := range engine.layers {
		for _, row := range layer.rules {
			publicIDs[row.id] = struct{}{}
		}
	}
	for _, row := range rows {
		if _, collision := publicIDs[row.id]; collision {
			return ambiguousRuleID(row.id)
		}
	}
	appendMemoryRows(engine, rows)
	if err := checkMemoryGrantSize(ctx, next.state, engine); err != nil {
		return err
	}
	if engine.unknown == api.UnknownClassify {
		index, err := buildClassifierNameIndex(engine)
		if err != nil {
			return err
		}
		engine.classifierNames = index
	}
	next.engine = engine
	return nil
}

func (s *Server) revokeMemoryGrantLocked(ctx context.Context, id string) (bool, error) {
	if ctx == nil || ctx.Err() != nil {
		return false, endedPrivateGrant()
	}
	current := s.currentEngine()
	next := copyGrantEngine(current)
	found := false
	for index := range next.layers {
		next.layers[index].rules = slices.DeleteFunc(next.layers[index].rules, func(row compiledRule) bool {
			remove := row.memoryOnly && row.id == id
			found = found || remove
			return remove
		})
	}
	if !found {
		return false, nil
	}
	// Removing authority need not rebuild the possible-name comparison index;
	// each hit still uses the same current matcher before fixed denial.
	s.ledger.syncPolicy(next)
	s.mu.Lock()
	s.engine = next
	s.mu.Unlock()
	if err := s.joinRevokedMemoryFlowsLocked(ctx, next); err != nil {
		return true, err
	}
	if s.holds != nil {
		s.holds.policyChanged(false)
	}
	s.wakeExpiry()
	return true, nil
}

func (s *Server) dropMemoryGrants() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine == nil {
		return
	}
	next := copyGrantEngine(s.engine)
	for index := range next.layers {
		next.layers[index].rules = slices.DeleteFunc(next.layers[index].rules, func(row compiledRule) bool { return row.memoryOnly })
	}
	// No matcher runs after session closure. Drop the possible-name index too,
	// so it cannot retain a sensitive target after the actor is stopped.
	next.classifierNames = nil
	s.engine = next
}

// Cancel and physically join only scopes that lost their last current
// authority. Cleanup closes done without reacquiring policyMu.
func (s *Server) joinRevokedMemoryFlowsLocked(ctx context.Context, next *Engine) error {
	var owners []activeFlow
	for _, flow := range s.flows.snapshot() {
		if !flowAllowed(next, s.dialer, flow) {
			owners = append(owners, flow)
		}
	}
	for _, owner := range owners {
		if owner.cancel == nil || owner.done == nil {
			return s.secretJoinUncertain()
		}
	}
	for _, owner := range owners {
		owner.cancel()
	}
	for _, owner := range owners {
		select {
		case <-owner.done:
		case <-ctx.Done():
			return s.secretJoinUncertain()
		}
	}
	return nil
}
