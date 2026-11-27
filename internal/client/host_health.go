package client

import (
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// appendClientHostHealthV1 changes only the owned advisory health vector.
// Neither a prepared record nor a cache inventory becomes admission evidence.
func appendClientHostHealthV1(facts *api.HostFacts, original Host, release *supply.Manifest, sent, received time.Time) {
	add := func(item api.Health) {
		for _, row := range facts.Health {
			if row.ID == item.ID {
				return
			}
		}
		facts.Health = append(facts.Health, item)
	}
	if prepared := original.Prepared; prepared != nil && facts.LevelKnown && prepared.Arch == facts.Arch && wallLevelRank(facts.Level) < wallLevelRank(prepared.Level) {
		levels := []string{}
		for _, level := range []string{"process", "gvisor", "microvm"} {
			if wallLevelRank(level) > wallLevelRank(facts.Level) && wallLevelRank(level) <= wallLevelRank(prepared.Level) {
				levels = append(levels, level)
			}
		}
		add(api.Health{ID: "level-drift", Severity: "blocks_new", Levels: levels, Message: "The host's current wall is weaker than at preparation.", Hint: "Run host doctor and prepare the original wall again before using it."})
	}
	if facts.RuntimeDigestsKnown && release != nil {
		present := make(map[string]bool, len(facts.RuntimeDigests))
		for _, digest := range facts.RuntimeDigests {
			present[digest] = true
		}
		missing := false
		for _, item := range release.Items {
			if item.Arch == facts.Arch && runtimecache.IsRuntime(item.Kind) && !present[item.SHA256] {
				missing = true
			}
		}
		if missing {
			add(api.Health{ID: "runtime-missing", Severity: "blocks_new", Levels: []string{"microvm"}, Message: "The host lacks runtime files named by this client's release.", Hint: "Run host prepare, or let the next new sandbox install this client's runtime."})
		}
	}
	if release != nil {
		for _, item := range release.Items {
			if item.Arch == facts.Arch && item.Kind == supply.KindVMM && item.EOL != "" && item.EOL < received.UTC().Format(time.DateOnly) {
				add(api.Health{ID: "runtime-eol", Severity: "warn", Message: "This client's pinned virtual machine runtime is past its end of fixes.", Hint: "Install a current client and end older sessions when they are idle."})
				break
			}
		}
	}
	// Bound clock comparison by both local endpoints. A slow request alone
	// cannot label an otherwise plausible clock as five minutes adrift.
	if !sent.IsZero() && !received.Before(sent) && (facts.At.Before(sent.Add(-5*time.Minute)) || facts.At.After(received.Add(5*time.Minute))) {
		add(api.Health{ID: "clock", Severity: "warn", Message: "The host's clock differs from this computer by more than five minutes.", Hint: "Check time synchronisation on this computer and on the host."})
	}
}

func cleanHostHealthV1(rows []api.Health) []api.Health {
	result := make([]api.Health, len(rows))
	for i, row := range rows {
		row.ID, row.Severity = CleanLine(row.ID), CleanLine(row.Severity)
		row.Message, row.Hint = CleanLine(row.Message), CleanLine(row.Hint)
		row.Levels = append([]string(nil), row.Levels...)
		for level := range row.Levels {
			row.Levels[level] = CleanLine(row.Levels[level])
		}
		result[i] = row
	}
	return result
}

func hostHealthDetailsV1(info *HostInfo) []Detail {
	var rows []Detail
	for _, health := range info.Health {
		if health.Severity != "warn" && health.Severity != "blocks_new" {
			continue
		}
		row := Detail{Label: "Health", Value: CleanDetail(health.Message)}
		if hint := CleanDetail(health.Hint); hint != "" {
			row.Note = "Try: " + hint
		}
		rows = append(rows, row)
	}
	return rows
}
