package cli

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// showModelUsage draws only an actual live sandbox-root meter projection.
// It never constructs a zero counter or attributes it to one member agent.
func showModelUsage(session client.Session) string {
	if session.Usage == nil {
		return ""
	}
	if !session.Alive || !api.ValidModelUsageV1(*session.Usage) {
		return "unknown (no live model usage)"
	}
	usage := session.Usage
	prefix := ""
	if usage.Saturated {
		prefix = "at least "
	}
	text := fmt.Sprintf("%s%d model tokens (sandbox-wide)", prefix, usage.Total.TotalTokens)
	if usage.Total.EstimatedRequests == 1 {
		text += ", 1 estimated request"
	} else if usage.Total.EstimatedRequests > 1 {
		text += fmt.Sprintf(", %d estimated requests", usage.Total.EstimatedRequests)
	}
	if usage.BudgetTokens > usage.Total.TotalTokens {
		text += fmt.Sprintf(", %d counted towards budget", usage.BudgetTokens)
	}
	return text
}
