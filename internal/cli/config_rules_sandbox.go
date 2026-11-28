package cli

import (
	"fmt"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func validateSandboxSettings(abbrev string, s client.Settings, ins *client.TOMLInspection) []configFinding {
	var out []configFinding
	addBad := func(pos client.ConfigPos, msg string) {
		out = append(out, configFinding{
			Path:     abbrev,
			Line:     pos.Line,
			Column:   pos.Column,
			Severity: "error",
			Code:     "bad_value",
			Message:  msg,
		})
	}
	sb := s.Sandbox
	if sb.Paste != "" && sb.Paste != "images-and-pdf" && sb.Paste != "any" {
		pos := ins.Pos("sandbox", "paste")
		out = append(out, configFinding{
			Path:        abbrev,
			Line:        pos.Line,
			Column:      pos.Column,
			Severity:    "error",
			Code:        "bad_value",
			Message:     fmt.Sprintf(`sandbox.paste must be "images-and-pdf" or "any", not %q`, sb.Paste),
			Suggestions: suggestFromStrings(sb.Paste, []string{"images-and-pdf", "any"}),
		})
	}
	if sb.CPUs < 0 {
		addBad(ins.Pos("sandbox", "cpus"), fmt.Sprintf("sandbox.cpus must be positive, not %d", sb.CPUs))
	}
	if sb.Memory != "" {
		if _, err := api.ParseMemoryMax(sb.Memory); err != nil {
			addBad(ins.Pos("sandbox", "memory"), "sandbox.memory: "+err.Error())
		}
	}
	if sb.DiskMax != "" {
		if _, err := api.ParseByteSize(sb.DiskMax, 1<<20); err != nil {
			addBad(ins.Pos("sandbox", "disk_max"), "sandbox.disk_max: "+err.Error())
		}
	}
	if sb.IdleStop != "" {
		if err := client.CheckIdleStop(sb.IdleStop); err != nil {
			addBad(ins.Pos("sandbox", "idle_stop"), "sandbox.idle_stop: "+err.Error())
		}
	}
	if sb.CopyMax != "" {
		if _, err := api.ParseByteSize(sb.CopyMax, 1<<10); err != nil {
			addBad(ins.Pos("sandbox", "copy_max"), "sandbox.copy_max: "+err.Error())
		}
	}
	if sb.SeedMax != "" {
		if _, err := api.ParseByteSize(sb.SeedMax, 1<<20); err != nil {
			addBad(ins.Pos("sandbox", "seed_max"), "sandbox.seed_max: "+err.Error())
		}
	}
	if sb.MaxRun != "" {
		if _, err := api.ParseRunDuration(sb.MaxRun); err != nil {
			addBad(ins.Pos("sandbox", "max_run"), "sandbox.max_run: "+err.Error())
		}
	}
	names := make([]string, 0, len(sb.Env))
	for k := range sb.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		pos := ins.Pos("sandbox", "env", k)
		if !client.ValidEnvName(k) {
			addBad(pos, fmt.Sprintf("%q is not a valid environment variable name in [sandbox.env]", k))
			continue
		}
		if _, isSecret := s.Secrets[k]; isSecret {
			addBad(pos, fmt.Sprintf("%q is in [secrets] and cannot be set in [sandbox.env]", k))
		}
	}
	if len(sb.CopyIn) > 0 {
		if _, err := client.ParseCopyInSpecs(sb.CopyIn, ""); err != nil {
			addBad(ins.Pos("sandbox", "copy_in"), "sandbox.copy_in: "+err.Error())
		}
	}
	return out
}

func validateHostSandbox(abbrev string, idx int, h client.Host, ins *client.TOMLInspection) []configFinding {
	if h.Sandbox == nil {
		return nil
	}
	var out []configFinding
	label := hostLabel(h, idx)
	addBad := func(pos client.ConfigPos, msg string) {
		out = append(out, configFinding{
			Path:     abbrev,
			Line:     pos.Line,
			Column:   pos.Column,
			Severity: "error",
			Code:     "bad_value",
			Message:  msg,
		})
	}
	sb := h.Sandbox
	if sb.CPUs < 0 {
		addBad(ins.HostPos(idx, "sandbox", "cpus"), fmt.Sprintf("host %q sandbox.cpus must be positive, not %d", label, sb.CPUs))
	}
	if sb.Memory != "" {
		if _, err := api.ParseMemoryMax(sb.Memory); err != nil {
			addBad(ins.HostPos(idx, "sandbox", "memory"), fmt.Sprintf("host %q sandbox.memory: %s", label, err.Error()))
		}
	}
	if sb.DiskMax != "" {
		if _, err := api.ParseByteSize(sb.DiskMax, 1<<20); err != nil {
			addBad(ins.HostPos(idx, "sandbox", "disk_max"), fmt.Sprintf("host %q sandbox.disk_max: %s", label, err.Error()))
		}
	}
	if sb.IdleStop != "" {
		if err := client.CheckIdleStop(sb.IdleStop); err != nil {
			addBad(ins.HostPos(idx, "sandbox", "idle_stop"), fmt.Sprintf("host %q sandbox.idle_stop: %s", label, err.Error()))
		}
	}
	if sb.MaxRun != "" {
		if _, err := api.ParseRunDuration(sb.MaxRun); err != nil {
			addBad(ins.HostPos(idx, "sandbox", "max_run"), fmt.Sprintf("host %q sandbox.max_run: %s", label, err.Error()))
		}
	}
	names := make([]string, 0, len(sb.Env))
	for k := range sb.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !client.ValidEnvName(k) {
			addBad(ins.HostPos(idx, "sandbox", "env", k),
				fmt.Sprintf("host %q sandbox.env has invalid variable name %q", label, k))
		}
	}
	if len(sb.CopyIn) > 0 {
		if _, err := client.ParseCopyInSpecs(sb.CopyIn, ""); err != nil {
			addBad(ins.HostPos(idx, "sandbox", "copy_in"), fmt.Sprintf("host %q sandbox.copy_in: %s", label, err.Error()))
		}
	}
	return out
}
