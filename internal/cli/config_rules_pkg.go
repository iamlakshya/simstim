package cli

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func validatePackageSettings(abbrev string, s client.Settings, ins *client.TOMLInspection) []configFinding {
	var out []configFinding
	if v := s.Sandbox.PackageManagers; v != "" {
		if _, err := api.ParsePackageManagers(v); err != nil {
			pos := ins.Pos("sandbox", "package_managers")
			out = append(out, configFinding{
				Path:        abbrev,
				Line:        pos.Line,
				Column:      pos.Column,
				Severity:    "error",
				Code:        "package_level_invalid",
				Message:     strings.TrimSuffix(api.PackageLevelMessage(v), "."),
				Suggestions: suggestFromStrings(v, []string{api.PackageLevelGuarded, api.PackageLevelStrict, api.PackageLevelOff}),
			})
		}
	}
	if v := s.Sandbox.System; v != "" {
		if _, err := api.ParseSandboxSystem(v); err != nil {
			pos := ins.Pos("sandbox", "system")
			out = append(out, configFinding{
				Path:        abbrev,
				Line:        pos.Line,
				Column:      pos.Column,
				Severity:    "error",
				Code:        "system_invalid",
				Message:     strings.TrimSuffix(api.SandboxSystemMessage(v), "."),
				Suggestions: suggestFromStrings(v, []string{api.SandboxSystemReset, api.SandboxSystemKeep}),
			})
		}
	}
	if v := s.Network.MinReleaseAge; v != "" {
		if _, _, err := api.ParseMinReleaseAge(v); err != nil {
			pos := ins.Pos("network", "min_release_age")
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "release_age_invalid",
				Message:  strings.TrimSuffix(api.ReleaseAgeMessage(v), "."),
			})
		}
	}
	if s.Network.ModelBudget < 0 {
		pos := ins.Pos("network", "model_budget")
		out = append(out, configFinding{
			Path:     abbrev,
			Line:     pos.Line,
			Column:   pos.Column,
			Severity: "error",
			Code:     "model_budget_invalid",
			Message:  strings.TrimSuffix(api.ModelBudgetMessage(s.Network.ModelBudget), "."),
		})
	}
	if v := s.Network.UploadBudget; v != "" {
		if _, _, err := api.ParseUploadBudget(v); err != nil {
			pos := ins.Pos("network", "upload_budget")
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "upload_budget_invalid",
				Message:  strings.TrimSuffix(api.UploadBudgetMessage(v), "."),
			})
		}
	}
	if v := s.Images.Fetch; v != "" {
		if _, err := api.ParseImagesFetch(v); err != nil {
			pos := ins.Pos("images", "fetch")
			out = append(out, configFinding{
				Path:        abbrev,
				Line:        pos.Line,
				Column:      pos.Column,
				Severity:    "error",
				Code:        "bad_value",
				Message:     fmt.Sprintf(`images.fetch must be "auto", "host" or "laptop", not %q`, v),
				Suggestions: suggestFromStrings(v, []string{api.ImagesFetchAuto, api.ImagesFetchHost, api.ImagesFetchLaptop}),
			})
		}
	}
	if v := s.Sandbox.Shell; v != "" {
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "\x00\r\n") {
			pos := ins.Pos("sandbox", "shell")
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "bad_value",
				Message:  "sandbox.shell must be a shell path or command name without control characters",
			})
		}
	}
	for _, rel := range s.Sandbox.Keep {
		if _, err := api.CheckKeepPath(rel); err != nil {
			pos := ins.Pos("sandbox", "keep")
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     api.KeepRefusedCode,
				Message:  err.Error(),
			})
		}
	}
	return out
}
