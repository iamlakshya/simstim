package egress

import (
	"context"
	"math"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// ModelUsageModeV1 is response framing after ordinary policy/decoding.
type ModelUsageModeV1 string

const (
	ModelUsageJSONV1      ModelUsageModeV1 = "json"
	ModelUsageSSEV1       ModelUsageModeV1 = "sse"
	ModelUsageWebSocketV1 ModelUsageModeV1 = "websocket"
)

// ModelUsageCompletionV1 reports only the counters committed by this turn.
type ModelUsageCompletionV1 struct {
	Counts    api.ModelUsageCountsV1
	Estimated bool
	Saturated bool
}

// ModelUsageTurnV1 observes one admitted logical request without altering
// payloads. Finish is idempotent even when cancellation and EOF race.
type ModelUsageTurnV1 struct {
	mu                   sync.Mutex
	meter                *ModelUsageMeterV1
	wire                 string
	key                  modelUsageKeyV1
	physicalRequest      bool // only an actual original origin observer can select this mode
	physicalRequestDone  chan struct{}
	physicalRequestEnded bool
	requestBytes         uint64
	responseBytes        uint64
	liveTokens           uint64
	parser               *modelUsageParserV1
	ignored              bool
	finished             bool
	byteOverflow         bool
	completion           ModelUsageCompletionV1
}

// ResponseV1 selects only framing. It grants no model request or network flow.
func (t *ModelUsageTurnV1) ResponseV1(mode ModelUsageModeV1) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ignored && !t.finished {
		t.parser.setMode(mode)
	}
}

// ForwardedV1 accepts only bytes actually written after decoding. Read-ahead,
// headers and encrypted wire bytes must never enter this observer.
func (t *ModelUsageTurnV1) ForwardedV1(payload []byte) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ignored || t.finished {
		return
	}
	if uint64(len(payload)) > math.MaxUint64-t.responseBytes {
		t.responseBytes, t.byteOverflow = math.MaxUint64, true
	} else {
		t.responseBytes += uint64(len(payload))
	}
	t.parser.feed(payload)
	t.syncKnownUsageLocked()
}

// FinishV1 commits reported usage only for a complete logical answer. A cut,
// malformed or missing usage becomes an explicitly counted byte estimate.
func (t *ModelUsageTurnV1) FinishV1(complete bool) ModelUsageCompletionV1 {
	if t == nil {
		return ModelUsageCompletionV1{}
	}
	t.mu.Lock()
	requestDone := t.physicalRequestDone
	t.mu.Unlock()
	if requestDone != nil {
		<-requestDone
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return t.completion
	}
	t.finished = true
	if t.ignored {
		return t.completion
	}
	counts, reported, saturated := t.parser.finish(complete)
	counts.Requests = 1
	if !reported {
		counts = api.ModelUsageCountsV1{Requests: 1, EstimatedRequests: 1}
		counts.TotalTokens, saturated = modelUsageEstimateV1(t.requestBytes, t.responseBytes)
	}
	saturated = saturated || t.byteOverflow
	t.completion = ModelUsageCompletionV1{Counts: counts, Estimated: !reported, Saturated: saturated}
	t.meter.commit(t.key, counts, saturated, t.liveTokens)
	return t.completion
}

func modelUsageEstimateV1(request, response uint64) (uint64, bool) {
	quotients := request/4 + response/4 // at most MaxUint64/2
	remainder := (request%4 + response%4 + 3) / 4
	if remainder > math.MaxUint64-quotients {
		return math.MaxUint64, true
	}
	return quotients + remainder, false
}

func (t *ModelUsageTurnV1) syncKnownUsageLocked() {
	if t.parser.has {
		counts, reported, saturated := t.parser.reportedCounts()
		if reported {
			known := max(t.liveTokens, counts.TotalTokens)
			t.meter.replaceLiveTokens(t.liveTokens, known, saturated)
			t.liveTokens = known
		}
	}
}

func (t *ModelUsageTurnV1) forwardedWebSocketEventV1(event modelUsageEventV1, size uint64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ignored || t.finished {
		return
	}
	if size > math.MaxUint64-t.responseBytes {
		t.responseBytes = math.MaxUint64
		t.byteOverflow = true
	} else {
		t.responseBytes += size
	}
	t.parser.recordParsedEvent(event, false)
	t.syncKnownUsageLocked()
}

func (t *ModelUsageTurnV1) setContext(ctx context.Context) {
	if t == nil || ctx == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ignored && !t.finished {
		t.parser.ctx = ctx
	}
}
