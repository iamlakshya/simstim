package egress

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// One ledger ownership covers canonical network/lock/deny admission, every
// capability precheck, and all acquisitions. Failure mutates no slots. The
// actual capabilities are acquired first, so the network matcher can reuse
// their owned once slots instead of spending an unrelated capability grant.
func (e *Engine) admitModelRequest(model *modelRequest, permit *Permit, needNetwork bool, mode evalMode) string {
	request, valid := canonicalModelRequest(model.request)
	if !valid || e.ledger == nil || e.modelGuard(request) == nil {
		return "model policy"
	}
	features := slices.Clone(model.features)
	if model.search && e.preset == api.PresetLocked && !slices.Contains(features, "web_search") {
		features = append(features, "web_search")
	}
	e.ledger.mu.Lock()
	defer e.ledger.mu.Unlock()
	if request.Now.IsZero() {
		request.Now = e.now()
	}
	if !e.validPermit(permit, mode) || mode != evalPrecheck && mode != evalReserve && mode != evalRevalidate {
		return "model policy"
	}
	networkCheck := evalPrecheck
	if mode == evalRevalidate || permit != nil && permit.committed {
		networkCheck = evalRevalidate
	}
	if !e.evaluateModelNetworkLocked(request, permit, networkCheck).Allow {
		return "model policy"
	}
	check := evalPrecheck
	if mode == evalRevalidate {
		check = evalRevalidate
	} else if permit != nil && permit.committed {
		check = evalModelPrecheck
	}
	plan, missing := e.modelCapabilityPlanLocked(request, features, permit, check)
	if missing != "" {
		return missing
	}
	if mode != evalReserve {
		return ""
	}
	// Late connected-WebSocket acquisition is capability-only. It cannot
	// invent a second network reservation or another TCP commit boundary.
	if needNetwork && permit.committed {
		return "model policy"
	}
	reserve := evalReserve
	if permit.committed {
		reserve = evalModelReserve
	}
	for _, rule := range plan {
		e.useOnce(rule, permit, reserve)
	}
	if needNetwork && !e.evaluateModelNetworkLocked(request, permit, evalReserve).Allow {
		// No other observer has seen this transaction. Release only this plan's
		// newly reserved slots before unlocking; never an existing owned lease.
		for _, rule := range plan {
			slot := rule.slot
			if slot != nil && slot.owner == permit && slot.state == slotReserved {
				slot.state, slot.owner = slotAvailable, nil
				delete(permit.owned, slot)
			}
		}
		return "model policy"
	}
	return ""
}
