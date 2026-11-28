package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// showUsage formats a session's model token usage and optional budget for
// `show`, without showing prices (isolation spec 17.8).
func showUsage(u *api.SessionUsage) string {
	if u == nil || (u.Total.Requests == 0 && u.Total.TotalTokens == 0 && u.Budget <= 0) {
		return ""
	}
	t := u.Total
	head := fmt.Sprintf("%d tokens", t.TotalTokens)
	if u.Budget > 0 {
		head = fmt.Sprintf("%d of %d tokens", t.TotalTokens, u.Budget)
	}
	if t.Requests == 0 {
		return head
	}

	var detail []string
	if t.InputTokens > 0 || t.OutputTokens > 0 {
		detail = append(detail, fmt.Sprintf("%d in", t.InputTokens), fmt.Sprintf("%d out", t.OutputTokens))
	}
	if t.CacheReadTokens > 0 {
		detail = append(detail, fmt.Sprintf("%d cached", t.CacheReadTokens))
	}
	if t.CacheCreationTokens > 0 {
		detail = append(detail, fmt.Sprintf("%d cache write", t.CacheCreationTokens))
	}
	if t.ReasoningTokens > 0 {
		detail = append(detail, fmt.Sprintf("%d reasoning", t.ReasoningTokens))
	}
	if t.WebSearchRequests > 0 {
		detail = append(detail, fmt.Sprintf("%d web search", t.WebSearchRequests))
	}

	reqWord := "requests"
	if t.Requests == 1 {
		reqWord = "request"
	}
	reqText := fmt.Sprintf("%d %s", t.Requests, reqWord)
	if t.EstimatedRequests > 0 {
		reqText += fmt.Sprintf(", %d estimated", t.EstimatedRequests)
	}
	if len(detail) == 0 {
		return fmt.Sprintf("%s (%s)", head, reqText)
	}
	return fmt.Sprintf("%s (%s; %s)", head, strings.Join(detail, ", "), reqText)
}

// showUsageModels formats per-model token totals in deterministic name order.
func showUsageModels(u *api.SessionUsage) string {
	if u == nil || len(u.ByModel) == 0 {
		return ""
	}
	names := make([]string, 0, len(u.ByModel))
	for name := range u.ByModel {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s: %d", name, u.ByModel[name].TotalTokens))
	}
	return strings.Join(parts, ", ")
}
