package egress

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// ModelUsageRequestV1 describes an already guarded logical model request.
// Bytes are decoded forwarded body bytes, never TLS or HTTP framing bytes.
type ModelUsageRequestV1 struct {
	Wire         string
	Path         string
	Model        string
	RequestBytes uint64
}

type modelUsageKeyV1 struct{ wire, model string }

// ModelUsageMeterV1 belongs to one actual Server/root.eg lifetime. Engine
// replacements keep this same object; labels never select a budget account.
type ModelUsageMeterV1 struct {
	mu           sync.Mutex
	total        api.ModelUsageCountsV1
	budgetTokens uint64 // finished conservative quota charges
	liveTokens   uint64 // current known provider counts, budget only
	models       map[modelUsageKeyV1]api.ModelUsageCountsV1
	unattributed api.ModelUsageCountsV1
	saturated    bool
	alerted      bool
}

// NewModelUsageMeterV1 makes an empty live meter. Only the Server constructor
// owns this lifetime; a public usage DTO cannot construct its provenance.
func NewModelUsageMeterV1() *ModelUsageMeterV1 {
	return &ModelUsageMeterV1{models: make(map[modelUsageKeyV1]api.ModelUsageCountsV1)}
}

// BeginV1 checks the current supplied budget before a logical request. It
// reserves no tokens: requests already in flight always finish and count.
func (m *ModelUsageMeterV1) BeginV1(request ModelUsageRequestV1, budget int64) (*ModelUsageTurnV1, *ModelBudgetRefusalV1, error) {
	if m == nil || budget < 0 || !validModelUsageWireV1(request.Wire) {
		return nil, nil, errors.New("invalid model accounting admission. Try: use a current model protocol and nonnegative token budget")
	}
	if !modelUsageInferencePathV1(request.Wire, request.Path) {
		return &ModelUsageTurnV1{ignored: true}, nil, nil
	}
	m.mu.Lock()
	refusal := m.refusalLocked(request.Wire, budget)
	m.mu.Unlock()
	if refusal != nil {
		return nil, refusal, nil
	}
	turn := &ModelUsageTurnV1{meter: m, wire: request.Wire, requestBytes: request.RequestBytes}
	// This bounds retained identity, not request admission. Long/unknown
	// labels are still charged exactly to the explicit unattributed bucket.
	if request.Model != "" && len(request.Model) <= 256 {
		turn.key = modelUsageKeyV1{wire: request.Wire, model: strings.Clone(request.Model)}
	}
	turn.parser = newModelUsageParserV1(request.Wire)
	return turn, nil, nil
}

// BudgetRefusalV1 checks the current policy budget, for example after an open
// WebSocket's current response. The meter never stores a policy replica.
func (m *ModelUsageMeterV1) BudgetRefusalV1(wire string, budget int64) *ModelBudgetRefusalV1 {
	if m == nil || !validModelUsageWireV1(wire) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refusalLocked(wire, budget)
}

func (m *ModelUsageMeterV1) refusalLocked(wire string, budget int64) *ModelBudgetRefusalV1 {
	used := m.budgetTokens
	if m.liveTokens > math.MaxUint64-used {
		m.saturated = true
		used = math.MaxUint64
	} else {
		used += m.liveTokens
	}
	if budget >= 0 && !m.saturated && (budget == 0 || used < uint64(budget)) {
		return nil
	}
	refusal := &ModelBudgetRefusalV1{Wire: wire, Used: used, Budget: budget, Saturated: m.saturated, Alert: !m.alerted}
	m.alerted = true
	return refusal
}

func validModelUsageWireV1(wire string) bool {
	return wire == "anthropic-messages" || wire == "openai-responses" || wire == "openai-chat-completions"
}

func (m *ModelUsageMeterV1) commit(key modelUsageKeyV1, counts api.ModelUsageCountsV1, saturated bool, live ...uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(live) > 0 {
		if live[0] > m.liveTokens {
			m.saturated = true
			m.liveTokens = 0
		} else {
			m.liveTokens -= live[0]
		}
	}
	charge := counts.TotalTokens
	if len(live) > 0 {
		charge = max(charge, live[0])
	}
	if charge > math.MaxUint64-m.budgetTokens {
		m.budgetTokens = math.MaxUint64
		m.saturated = true
	} else {
		m.budgetTokens += charge
	}

	if m.models == nil {
		m.models = make(map[modelUsageKeyV1]api.ModelUsageCountsV1)
	}
	var overflow bool
	m.total, overflow = api.AddModelUsageCountsV1(m.total, counts)
	m.saturated = m.saturated || saturated || overflow
	if key.model != "" {
		previous, exists := m.models[key]
		if exists || len(m.models) < api.ModelUsageModelLimitV1 {
			m.models[key], overflow = api.AddModelUsageCountsV1(previous, counts)
			m.saturated = m.saturated || overflow
			return
		}
	}
	m.unattributed, overflow = api.AddModelUsageCountsV1(m.unattributed, counts)
	m.saturated = m.saturated || overflow
}

// SnapshotV1 returns independent descriptive counters, never admission state.
func (m *ModelUsageMeterV1) SnapshotV1() api.ModelUsageV1 {
	return m.snapshotWithSourceV1(nil)
}

func (m *ModelUsageMeterV1) snapshotWithSourceV1(source *auditSecrets) api.ModelUsageV1 {
	if m == nil {
		return api.ModelUsageV1{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]modelUsageKeyV1, 0, len(m.models))
	for key := range m.models {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].wire != keys[j].wire {
			return keys[i].wire < keys[j].wire
		}
		return keys[i].model < keys[j].model
	})
	result := api.ModelUsageV1{Version: api.ModelUsageVersionV1, Scope: "sandbox", Total: m.total, Unattributed: m.unattributed, Saturated: m.saturated}
	result.BudgetTokens = m.budgetTokens
	if m.liveTokens > math.MaxUint64-result.BudgetTokens {
		result.BudgetTokens = math.MaxUint64
		result.Saturated = true
	} else {
		result.BudgetTokens += m.liveTokens
	}

	for _, key := range keys {
		label := cleanAuditText(key.model, source, 96)
		if label == "" {
			label = "model redacted"
		}
		result.Models = append(result.Models, api.ModelUsageModelV1{Protocol: key.wire, Model: label, Redacted: label != key.model, Counts: m.models[key]})
	}
	return result
}

func modelUsageInferencePathV1(wire, path string) bool {
	switch wire {
	case "anthropic-messages":
		return path == "/v1/messages"
	case "openai-responses":
		return path == "/v1/responses" || path == "/backend-api/codex/responses"
	case "openai-chat-completions":
		return path == "/v1/chat/completions"
	}
	return false
}

func (m *ModelUsageMeterV1) replaceLiveTokens(old, current uint64, saturated bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old > m.liveTokens {
		m.saturated = true
		return
	}
	remaining := m.liveTokens - old
	if current > math.MaxUint64-remaining {
		m.liveTokens = math.MaxUint64
		m.saturated = true
		return
	}
	m.liveTokens = remaining + current
	m.saturated = m.saturated || saturated
}
