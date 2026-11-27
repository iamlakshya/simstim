package egress

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// This is bounded correlation on one existing originRequest. All authority,
// permits, quota and source ownership stay in that request and its one meter.
type modelWebSocketUsage struct{ state *modelWebSocketUsageState }
type modelWebSocketUsageState struct {
	scope                 *originRequest
	mu                    sync.Mutex
	changed               chan struct{}
	records               []*modelRequest
	reserved              int
	active                map[[32]byte]*modelRequest
	retired               [maxActiveFlows][32]byte
	retiredN, retiredNext int
	fail                  func(error)
	exhausted, closed     bool
	budget                *ModelBudgetRefusalV1
	audits                []AuditEntry
	wire                  modelUsageWebSocketWire
}

func (modelWebSocketUsage) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<WebSocket model usage redacted>")
}
func (*modelWebSocketUsageState) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<WebSocket model usage state redacted>")
}
func newModelWebSocketUsage(scope *originRequest) *modelWebSocketUsage {
	return &modelWebSocketUsage{state: &modelWebSocketUsageState{scope: scope, changed: make(chan struct{}), active: make(map[[32]byte]*modelRequest)}}
}
func (state *modelWebSocketUsageState) wakeLocked() {
	close(state.changed)
	state.changed = make(chan struct{})
}
func (usage *modelWebSocketUsage) reserve(ctx context.Context) error {
	state := usage.state
	for {
		state.mu.Lock()
		if state.closed {
			state.mu.Unlock()
			return modelWebSocketRefusal{line: "model request ended. Try: reconnect against current policy."}
		}
		if state.exhausted {
			state.mu.Unlock()
			if err := usage.waitEmpty(ctx); err != nil {
				return err
			}
			return modelWebSocketRefusal{line: "model token budget reached. Try: raise the budget, then reconnect."}
		}
		if len(state.records)+state.reserved < maxActiveFlows {
			state.reserved++
			state.mu.Unlock()
			return nil
		}
		changed := state.changed
		state.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (usage *modelWebSocketUsage) release() {
	state := usage.state
	state.mu.Lock()
	state.reserved--
	state.wakeLocked()
	state.mu.Unlock()
}
func (usage *modelWebSocketUsage) add(model *modelRequest) bool {
	state := usage.state
	state.mu.Lock()
	defer state.mu.Unlock()
	state.reserved--
	if state.closed {
		state.wakeLocked()
		return false
	}
	state.records = append(state.records, model)
	state.wakeLocked()
	return true
}
func (usage *modelWebSocketUsage) waitEmpty(ctx context.Context) error {
	state := usage.state
	for {
		state.mu.Lock()
		empty := len(state.records) == 0 && state.reserved == 0
		changed := state.changed
		state.mu.Unlock()
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (usage *modelWebSocketUsage) attach(fail func(error)) {
	state := usage.state
	state.mu.Lock()
	state.fail = fail
	state.mu.Unlock()
}
func (usage *modelWebSocketUsage) checkBudget() {
	usage.checkBudgetContext(usage.state.scope.ctx)
}

func (usage *modelWebSocketUsage) checkBudgetContext(ctx context.Context) {
	state := usage.state
	s := state.scope.s
	if ctx == nil || s.policyMu.LockContext(ctx) != nil {
		return
	}
	source := s.currentMembers()
	var refusal *ModelBudgetRefusalV1
	if source != nil {
		refusal = s.usage.BudgetRefusalV1("openai-responses", modelBudget(source.metadata.Policy))
	}
	s.policyMu.Unlock()
	if refusal == nil {
		return
	}
	state.mu.Lock()
	state.exhausted = true
	if state.budget == nil {
		state.budget = refusal
	}
	empty := len(state.records) == 0 && state.reserved == 0
	fail := state.fail
	state.wakeLocked()
	state.mu.Unlock()
	if empty && fail != nil {
		fail(modelWebSocketRefusal{line: "model token budget reached. Try: raise the budget, then reconnect."})
	}
}
func (usage *modelWebSocketUsage) refuseBudget(ctx context.Context, refusal *ModelBudgetRefusalV1) error {
	state := usage.state
	state.mu.Lock()
	state.exhausted = true
	if state.budget == nil {
		state.budget = refusal
	}
	state.wakeLocked()
	state.mu.Unlock()
	if err := usage.waitEmpty(ctx); err != nil {
		return err
	}
	return modelWebSocketRefusal{line: "model token budget reached. Try: raise the budget, then reconnect."}
}
func (usage *modelWebSocketUsage) close() {
	state := usage.state
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
	state.closed = true
	// An incomplete logical payload is charged only as an estimate, without
	// attributing unknown response identities to a model label.
	if len(state.wire.message) > 0 && len(state.records) > 0 {
		model := state.records[0]
		model.usage.mu.Lock()
		model.usage.key = modelUsageKeyV1{}
		model.usage.mu.Unlock()
		model.usage.forwardedWebSocketEventV1(modelUsageEventV1{}, uint64(len(state.wire.message)))
	}
	clear(state.wire.message)
	state.wire.message = nil
	for _, model := range state.records {
		completion := model.usage.FinishV1(false)
		entry := AuditEntry{Host: state.scope.request.Host, Port: state.scope.request.Port, Method: state.scope.request.Method, Path: state.scope.request.Path, Mode: "intercept", Status: 200, Decision: "allow", Rule: "model:response"}
		applyUsageAudit(&entry, completion)
		state.audits = append(state.audits, entry)
	}
	audits := state.audits
	state.audits = nil
	state.records = nil
	clear(state.active)
	budget := state.budget
	state.wakeLocked()
	state.mu.Unlock()
	for _, entry := range audits {
		state.scope.s.emitUsageAudit(state.scope.ctx, entry)
	}
	state.scope.s.emitModelBudget(state.scope.ctx, budget)
}

func (s *Server) admitModelWebSocketUsage(ctx context.Context, scope *originRequest, model *modelRequest) error {
	if ctx == nil || scope == nil || model == nil {
		return modelWebSocketRefusal{line: "model request has no live owner. Try: reconnect with matching helpers."}
	}
	scope.mu.Lock()
	usage := scope.usageWS
	scope.mu.Unlock()
	if usage == nil {
		return modelWebSocketRefusal{line: "model accounting is unavailable. Try: reconnect with matching helpers."}
	}
	if err := usage.reserve(ctx); err != nil {
		return err
	}
	if s.policyMu.LockContext(ctx) != nil {
		usage.release()
		return ctx.Err()
	}
	source := s.currentMembers()
	if source == nil || !scope.allowed(s.currentEngine(), s.dialer) {
		s.policyMu.Unlock()
		usage.release()
		return modelWebSocketRefusal{line: "model request changed. Try: reconnect against current policy."}
	}
	turn, refusal, err := s.usage.BeginV1(ModelUsageRequestV1{Wire: modelUsageWire(model), Path: model.request.Path, Model: model.usageModel, RequestBytes: model.requestBytes}, modelBudget(source.metadata.Policy))
	s.policyMu.Unlock()
	if err != nil {
		usage.release()
		return modelWebSocketRefusal{line: "model accounting is unavailable. Try: reconnect with matching helpers."}
	}
	if refusal != nil {
		usage.release()
		return usage.refuseBudget(ctx, refusal)
	}
	turn.setContext(ctx)
	if err := turn.beginPhysicalWebSocketRequestV1(); err != nil {
		usage.release()
		turn.FinishV1(false)
		return modelWebSocketRefusal{line: "model request writer is unavailable. Try: reconnect with matching helpers."}
	}
	turn.ResponseV1(ModelUsageWebSocketV1)
	model.usage = turn
	if !usage.add(model) {
		turn.endPhysicalWebSocketRequestV1()
		turn.FinishV1(false)
		return modelWebSocketRefusal{line: "model request ended. Try: reconnect against current policy."}
	}
	return nil
}
