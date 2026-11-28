package cli

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/sandbox"
)

func validateSandboxCaches(abbrev string, rules []string, pos client.ConfigPos, label string) []configFinding {
	if len(rules) == 0 {
		return nil
	}
	var out []configFinding
	for _, rule := range rules {
		if _, _, err := sandbox.ParseCacheRule(rule); err != nil {
			msg := err.Error()
			if before, _, ok := strings.Cut(msg, ". Try: "); ok {
				msg = before
			}
			msg = strings.TrimSuffix(msg, ".")
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "cache_path_invalid",
				Message:  fmt.Sprintf("%s: %s", label, msg),
			})
		}
	}
	return out
}
