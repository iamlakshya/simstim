package egress

import (
	"context"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

// This is the original inspected message reader and original admitted turn.
// The existing client direction borrows that turn while forwarding this one
// message, then releases its writer join on every completion/error/cancel cut.
type modelUsageWebSocketRequestBody struct {
	io.ReadCloser
	turn    *ModelUsageTurnV1
	written func()
}

func (*modelUsageWebSocketRequestBody) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, api.Redacted)
}
func (b *modelUsageWebSocketRequestBody) physicalModelRequestV1() *ModelUsageTurnV1 { return b.turn }

func (t *ModelUsageTurnV1) beginPhysicalWebSocketRequestV1() error {
	if err := t.beginPhysicalRequestV1(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished || t.physicalRequestDone != nil {
		return errPhysicalH2Binding
	}
	t.physicalRequestDone = make(chan struct{})
	return nil
}

func (t *ModelUsageTurnV1) endPhysicalWebSocketRequestV1() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.physicalRequestDone != nil && !t.physicalRequestEnded {
		t.physicalRequestEnded = true
		close(t.physicalRequestDone)
	}
}

func (b *modelUsageWebSocketRequestBody) physicalModelRequestFinishedV1() {
	b.turn.endPhysicalWebSocketRequestV1()
	if b.written != nil {
		b.written()
	}
}

func (t *ModelUsageTurnV1) physicalRequestReadyV1() bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	done := t.physicalRequestDone
	t.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// Runs on the existing original client direction after its native message
// writes/filter cleanup. It can settle its parked row without a new worker.
func (usage *modelWebSocketUsage) requestWritten(ctx context.Context, model *modelRequest) {
	state := usage.state
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
	if model.usagePendingFinish {
		state.finishLocked(model, model.usagePendingComplete)
	}
	audits := state.audits
	state.audits = nil
	state.mu.Unlock()
	for _, entry := range audits {
		state.scope.s.emitUsageAudit(state.scope.ctx, entry)
	}
	usage.checkBudgetContext(ctx)
}
