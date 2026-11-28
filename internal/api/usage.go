package api

import (
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"
)

// Defaults and hints for session network budgets (isolation spec 7.2 and 17.8).
const (
	DefaultUploadBudget      = "16M"
	DefaultUploadBudgetBytes = 16 << 20

	ModelBudgetHint  = "Set network.model_budget to 0 (no limit) or a positive integer token count in config.toml"
	UploadBudgetHint = `Set network.upload_budget to "0" (no limit) or a size such as "16M" in config.toml`
)

// TokenUsage holds model token counts reported by a provider response or
// accumulated across a session (isolation spec 17.8).
type TokenUsage struct {
	Model               string `json:"model,omitempty"`
	InputTokens         int64  `json:"input_tokens,omitempty"`
	CacheCreationTokens int64  `json:"cache_creation_input_tokens,omitempty"`
	CacheReadTokens     int64  `json:"cache_read_input_tokens,omitempty"`
	OutputTokens        int64  `json:"output_tokens,omitempty"`
	ReasoningTokens     int64  `json:"reasoning_tokens,omitempty"`
	TotalTokens         int64  `json:"total_tokens,omitempty"`
	WebSearchRequests   int64  `json:"web_search_requests,omitempty"`
	Estimated           bool   `json:"estimated,omitempty"`
	Requests            int64  `json:"requests,omitempty"`
	EstimatedRequests   int64  `json:"estimated_requests,omitempty"`
}

// SessionUsage is the session-wide model token count and configured budget
// returned by show, ls --json and the egress usage query (isolation spec 17.8).
type SessionUsage struct {
	Total   TokenUsage            `json:"total"`
	ByModel map[string]TokenUsage `json:"by_model,omitempty"`
	Budget  int64                 `json:"budget,omitempty"`
}

// Clone returns a deep copy of u, or nil if u is nil.
func (u *SessionUsage) Clone() *SessionUsage {
	if u == nil {
		return nil
	}
	cp := *u
	if len(u.ByModel) > 0 {
		cp.ByModel = maps.Clone(u.ByModel)
	} else {
		cp.ByModel = nil
	}
	return &cp
}

// AddUsage folds src into dst, incrementing request and estimated counters.
func AddUsage(dst *TokenUsage, src TokenUsage) {
	dst.InputTokens += src.InputTokens
	dst.CacheCreationTokens += src.CacheCreationTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.OutputTokens += src.OutputTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.TotalTokens += src.TotalTokens
	dst.WebSearchRequests += src.WebSearchRequests
	if src.Requests > 0 {
		dst.Requests += src.Requests
	} else {
		dst.Requests++
	}
	switch {
	case src.EstimatedRequests > 0:
		dst.EstimatedRequests += src.EstimatedRequests
	case src.Estimated:
		dst.EstimatedRequests++
	}
}

// ModelBudgetMessage returns the spec error sentence for an invalid
// network.model_budget value, without a trailing Try: hint.
func ModelBudgetMessage(tokens int64) string {
	return fmt.Sprintf("network.model_budget is %d, and it must be 0 or a positive number of tokens.", tokens)
}

// CheckModelBudget validates network.model_budget (0 for no limit, or a
// positive integer token count).
func CheckModelBudget(tokens int64) error {
	if tokens < 0 {
		return fmt.Errorf("%s Try: %s", ModelBudgetMessage(tokens), ModelBudgetHint)
	}
	return nil
}

// UploadBudgetMessage returns the error sentence for an invalid
// network.upload_budget value, without a trailing Try: hint.
func UploadBudgetMessage(raw string) string {
	return fmt.Sprintf(`network.upload_budget is %q, and it must be "0" or a size such as "16M".`, raw)
}

var uploadBudgetSize = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?) ?(b|[kmgt](?:ib?|b)?)$`)

// ParseUploadBudget validates network.upload_budget (spec 7.2: empty defaults
// to "16M", "0" disables the cap, and positive sizes use powers of 1024 such
// as "512K", "16M", "16MiB" or "1G").
func ParseUploadBudget(raw string) (bytes int64, canonical string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultUploadBudgetBytes, DefaultUploadBudget, nil
	}
	s := strings.ToLower(trimmed)
	if s == "0" || s == "0b" || s == "0k" || s == "0m" || s == "0g" {
		return 0, "0", nil
	}
	m := uploadBudgetSize.FindStringSubmatch(s)
	if m == nil {
		return 0, "", fmt.Errorf("%s Try: %s", UploadBudgetMessage(raw), UploadBudgetHint)
	}
	n, convErr := strconv.ParseFloat(m[1], 64)
	if convErr != nil || n < 0 {
		return 0, "", fmt.Errorf("%s Try: %s", UploadBudgetMessage(raw), UploadBudgetHint)
	}
	if n == 0 {
		return 0, "0", nil
	}
	unit := m[2]
	var mult float64 = 1
	switch unit[0] {
	case 'b':
		mult = 1
	case 'k':
		mult = 1 << 10
	case 'm':
		mult = 1 << 20
	case 'g':
		mult = 1 << 30
	case 't':
		mult = 1 << 40
	}
	val := n * mult
	if val < 1 || val >= 1<<62 {
		return 0, "", fmt.Errorf("%s Try: %s", UploadBudgetMessage(raw), UploadBudgetHint)
	}
	return int64(val), trimmed, nil
}
