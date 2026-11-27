package egress

import "slices"

func terminalModelUsageEvent(kind string) bool {
	return kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed"
}

// The documented stream_id names an ordered, non-overlapping lane. Different
// lanes can interleave; the implicit default lane is distinct from every name.
func modelUsageSameLane(model *modelRequest, event modelUsageEventV1) bool {
	return model.hasUsageStreamID == event.hasStreamID && (!event.hasStreamID || model.usageStreamID == event.streamID)
}
func (state *modelWebSocketUsageState) pendingLocked(event modelUsageEventV1) *modelRequest {
	for _, model := range state.records {
		if !modelUsageSameLane(model, event) {
			continue
		}
		if !model.hasUsageResponseID && !model.usagePendingFinish {
			return model
		}
	}
	return nil
}
func (state *modelWebSocketUsageState) retiredLocked(id [32]byte) bool {
	for index := 0; index < state.retiredN; index++ {
		if state.retired[index] == id {
			return true
		}
	}
	return false
}
func (state *modelWebSocketUsageState) finishLocked(model *modelRequest, complete bool) {
	// A provider can end one lane while its request writer still owns native
	// IO. Park only this original bounded row; never wait under socket mu or
	// stop forwarding/accounting other admitted lanes.
	if !model.usage.physicalRequestReadyV1() {
		if !model.usagePendingFinish {
			model.usagePendingFinish, model.usagePendingComplete = true, complete
		}
		return
	}
	if model.usagePendingFinish {
		complete = model.usagePendingComplete
		model.usagePendingFinish = false
	}
	if model.hasUsageResponseID {
		delete(state.active, model.usageResponseID)
		state.retired[state.retiredNext] = model.usageResponseID
		state.retiredNext = (state.retiredNext + 1) % maxActiveFlows
		state.retiredN = min(state.retiredN+1, maxActiveFlows)
	}
	completion := model.usage.FinishV1(complete)
	entry := AuditEntry{Host: state.scope.request.Host, Port: state.scope.request.Port, Method: state.scope.request.Method, Path: state.scope.request.Path, Mode: "intercept", Status: 200, Decision: "allow", Rule: "model:response"}
	applyUsageAudit(&entry, completion)
	state.audits = append(state.audits, entry)
	state.records = slices.DeleteFunc(state.records, func(candidate *modelRequest) bool { return candidate == model })
	state.wakeLocked()
}
func (state *modelWebSocketUsageState) errorOwnerLocked(event modelUsageEventV1) *modelRequest {
	// Current Responses request-scoped errors echo the named lane. Its
	// first record owns the error under the documented per-lane FIFO rule.
	if event.hasStreamID {
		for _, model := range state.records {
			if modelUsageSameLane(model, event) {
				return model
			}
		}
		return nil
	}
	var selected *modelRequest
	for _, model := range state.records {
		if model.hasUsageStreamID || !model.hasUsageEventID {
			continue
		}
		// Preserve the legacy nested client-event extension. Current Responses
		// uses echoed named lanes above; its outer event ID cannot select a
		// request, and an unscoped ambiguous error remains fail-closed.
		matches := event.hasRequestEventID && model.usageEventID == event.requestEventID
		if matches {
			if selected != nil {
				return nil
			}
			selected = model
		}
	}
	return selected
}

// Called only after the complete decoded logical payload was successfully
// written to the guest. response.created introduces the provider's identity;
// later events must use that exact active identity. Unknown terminal IDs do
// not consume another pending request or count its tokens.
func (state *modelWebSocketUsageState) messageLocked(raw []byte) error {
	event, err := parseModelUsageEventContextV1(state.scope.ctx, raw)
	if err != nil {
		event, err = modelUsageWebSocketIdentityV1(state.scope.ctx, raw)
		event.badUsage = true
	}
	if err != nil {
		return modelWebSocketRefusal{line: "model response identity is invalid. Try: reconnect against a supported model protocol."}
	}
	if event.kind == "error" {
		model := state.errorOwnerLocked(event)
		if model == nil {
			return modelWebSocketRefusal{line: "model error has no exact request or lane. Try: reconnect against a supported model protocol."}
		}
		if model.usagePendingFinish {
			return nil
		}
		model.usage.forwardedWebSocketEventV1(event, uint64(len(raw)))
		state.finishLocked(model, false)
		return nil
	}
	var model *modelRequest
	if event.hasResponseID {
		if state.retiredLocked(event.responseID) {
			return nil
		}
		model = state.active[event.responseID]
		if model == nil && event.kind == "response.created" {
			model = state.pendingLocked(event)
			if model == nil {
				return nil
			}
			model.usageResponseID, model.hasUsageResponseID = event.responseID, true
			state.active[event.responseID] = model
		}
		if model == nil {
			return nil
		}
		if !modelUsageSameLane(model, event) {
			return modelWebSocketRefusal{line: "model response lane differs from its original request. Try: reconnect against a supported model protocol."}
		}
	} else {
		// ID-less events can select only one record in the echoed lane.
		// An unknown named lane never falls back to the default lane.
		for _, candidate := range state.records {
			if !modelUsageSameLane(candidate, event) {
				continue
			}
			if model != nil {
				return nil
			}
			model = candidate
		}
		if model == nil {
			return nil
		}
	}
	if model.usagePendingFinish {
		return nil
	}
	model.usage.forwardedWebSocketEventV1(event, uint64(len(raw)))
	if terminalModelUsageEvent(event.kind) {
		state.finishLocked(model, !event.badUsage)
	}
	return nil
}

func (usage *modelWebSocketUsage) observe(payload []byte) {
	state := usage.state
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
	err := state.wire.feed(payload, state.messageLocked)
	fail := state.fail
	audits := state.audits
	state.audits = nil
	state.mu.Unlock()
	for _, entry := range audits {
		state.scope.s.emitUsageAudit(state.scope.ctx, entry)
	}
	if err != nil {
		if fail != nil {
			fail(err)
		}
		return
	}
	usage.checkBudget()
}
