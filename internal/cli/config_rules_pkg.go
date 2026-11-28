package cli

import (
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
	return out
}
