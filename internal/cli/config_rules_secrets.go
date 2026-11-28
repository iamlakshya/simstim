package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
)

func validateSecrets(abbrev string, secrets map[string]any, ins *client.TOMLInspection) []configFinding {
	if len(secrets) == 0 {
		return nil
	}
	names := make([]string, 0, len(secrets))
	for k := range secrets {
		names = append(names, k)
	}
	sort.Strings(names)

	var out []configFinding
	for _, name := range names {
		pos := ins.Pos("secrets", name)
		if !client.ValidSecretName(name) {
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "bad_value",
				Message:  fmt.Sprintf("%q cannot name a secret in [secrets]", name),
			})
		}
		raw := secrets[name]
		switch raw.(type) {
		case string, map[string]any:
		default:
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "wrong_type",
				Message:  fmt.Sprintf("secret %q in [secrets] must be a command string or a [secrets.%s] table", name, name),
			})
			continue
		}
		cfg, err := client.ParseSecretConfig(name, raw)
		if err != nil {
			code := "bad_value"
			if strings.HasPrefix(err.Error(), "unknown key ") {
				code = "unknown_key"
			} else if strings.Contains(err.Error(), " must be ") {
				code = "wrong_type"
			}
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     code,
				Message:  err.Error(),
			})
			continue
		}
		if strings.TrimSpace(cfg.Run) == "" {
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "bad_value",
				Message:  fmt.Sprintf("secret %q in [secrets] has an empty command", name),
			})
		}
	}
	return out
}
