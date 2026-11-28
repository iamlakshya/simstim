package client

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

const detailsDot = " · "

// HostDetails holds the saved and live lines of a host's details, shared by
// `host show` and the launcher's host details sheet (cli-spec 11.2, rule 5).
type HostDetails struct {
	Name         string
	Target       string
	SSHConfig    string
	Trusted      bool
	WallFloor    string
	Machine      *Machine
	Labels       []string
	Secrets      []string
	Reachable    bool
	SystemParts  []string
	System       string
	Helper       string
	Agents       []string
	Limits       string
	SessionCount int
}

// BuildHostDetails builds the shared host details from a saved host, its live
// doctor info (nil when unreachable), and its session count. Every string from
// the host is cleaned and capped to 200 runes.
func BuildHostDetails(h Host, info *HostInfo, sessionCount int) HostDetails {
	d := HostDetails{
		Name:         h.Name,
		Target:       h.Target(),
		SSHConfig:    h.SSHConfig,
		Trusted:      h.Trusted,
		WallFloor:    h.WallFloor,
		Machine:      h.Machine,
		Secrets:      append([]string(nil), h.Secrets...),
		SessionCount: sessionCount,
	}
	if len(h.Labels) > 0 {
		keys := make([]string, 0, len(h.Labels))
		for k := range h.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d.Labels = make([]string, 0, len(keys))
		for _, k := range keys {
			d.Labels = append(d.Labels, k+"="+h.Labels[k])
		}
	}
	if info == nil {
		return d
	}
	d.Reachable = true
	clean := CleanHostInfo(*info)
	distro := api.CleanText(clean.Distro, 200)
	arch := api.CleanText(clean.Arch, 200)
	ver := api.CleanText(clean.Version, 200)
	for _, s := range []string{
		distro,
		arch,
		pluralCount(clean.CPUs, "CPU", "CPUs"),
		formatBinarySize(clean.MemTotal),
		formatBinarySize(clean.DiskFree) + " free",
	} {
		if s != "" && !strings.HasPrefix(s, "0 ") {
			d.SystemParts = append(d.SystemParts, s)
		}
	}
	d.System = strings.Join(d.SystemParts, detailsDot)
	d.Helper = brand.HostBinary + " " + ver
	if len(clean.Agents) > 0 {
		names := make([]string, 0, len(clean.Agents))
		for k := range clean.Agents {
			label := api.CleanText(k, 200)
			if v := api.CleanText(clean.AgentVersions[k], 200); v != "" {
				label += " " + v
			}
			names = append(names, label)
		}
		sort.Strings(names)
		d.Agents = names
	}
	d.Limits = formatBinarySize(clean.MemoryMax) + " for sessions" + detailsDot + pluralCount(sessionCount, "session", "sessions")
	return d
}

func pluralCount(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func formatBinarySize(b int64) string {
	const mib, gib = 1 << 20, 1 << 30
	switch {
	case b >= gib:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(b)/gib), ".0") + " GiB"
	case b >= mib:
		return fmt.Sprintf("%d MiB", b/mib)
	default:
		return fmt.Sprintf("%d KiB", b>>10)
	}
}
