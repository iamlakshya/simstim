package client

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// The details of one host (docs/contributing/cli-spec.md 11.2). The
// launcher's host details and `host show` both take their lines from
// HostDetails, so they show the same facts in the same words.

// Dot separates the facts on one line.
const Dot = " · "

// DetailMax is the most runes of one value that the details show.
const DetailMax = 200

// CleanDetail is s as the details show it: without control or hidden
// characters, and at most DetailMax runes. A host sends most of these
// values, and an agent's own --version output reaches them, so none may
// reach the terminal as it is (isolation spec 14.15).
func CleanDetail(s string) string { return api.CleanText(s, DetailMax) }

// Detail is one line of a host's details: a label and its value.
type Detail struct {
	// Label names the line, in sentence case, such as "Route".
	Label string
	// Value is the line's value. A list, such as the agents, is in Items
	// instead.
	Value string
	// Items is a value that is a list. The launcher wraps it between its
	// items, and `host show` joins it with dots.
	Items []string
	// Note follows the value, such as the extra SSH config of the route.
	// The launcher shows it faint.
	Note string
}

// Text is the value of d on one line: its items joined with dots, and its
// note after two spaces.
func (d Detail) Text() string {
	v := d.Value
	if len(d.Items) > 0 {
		v = strings.Join(d.Items, Dot)
	}
	if d.Note != "" {
		v += "  " + d.Note
	}
	return v
}

// HostDetails are the facts about one host: what this computer saved, and
// what the host said about itself when it answered.
type HostDetails struct {
	Host Host
	// Tier is the host's tier on this computer, or TierUnknown when unset.
	Tier Tier
	// IDChanged is true when the host answered with a different host ID and
	// its tier has not been set or rekeyed since.
	IDChanged bool
	// Info is what the host said about itself, or nil when it has not
	// answered.
	Info *HostInfo
	// Sessions counts the host's sessions, when Info is set.
	Sessions int
}

// Lines are the saved lines, then the lines of what the host said.
func (d HostDetails) Lines() []Detail { return append(d.Saved(), d.Facts()...) }

// Saved are the lines of the saved entry: the route, the labels, the
// machine, its idle time, the provider script, and the names of the
// host's secrets. A line without a value is left out.
func (d HostDetails) Saved() []Detail {
	h := d.Host
	var out []Detail
	if h.SSH != "" {
		r := Detail{Label: "Route", Value: CleanDetail(h.SSH)}
		if h.SSHConfig != "" {
			r.Note = "with " + CleanDetail(h.SSHConfig)
		}
		out = append(out, r)
	}
	if d.Tier.Valid() {
		td := Detail{Label: "Tier", Value: d.Tier.String()}
		if d.IDChanged {
			td.Note = "host ID changed"
		}
		out = append(out, td)
	}
	if len(h.Labels) > 0 {
		labels := make([]string, 0, len(h.Labels))
		for k, v := range h.Labels {
			labels = append(labels, CleanDetail(k+"="+v))
		}
		sort.Strings(labels)
		out = append(out, Detail{Label: "Labels", Items: labels})
	}
	if m := h.Machine; m != nil {
		md := Detail{Label: "Machine", Value: CleanDetail(m.Provider + " " + m.ID)}
		switch {
		case m.Image != "":
			md.Note = "from " + CleanDetail(m.Image)
		case m.Template != "":
			md.Note = "from the template " + CleanDetail(m.Template)
		}
		out = append(out, md)
		if m.IdleStop != "" {
			out = append(out, Detail{Label: "Idle", Value: CleanDetail(m.IdleStop)})
		}
	}
	if p := h.Provider; p != nil && p.Script != "" {
		pd := Detail{Label: "Provider", Value: CleanDetail(p.Script)}
		if p.WakeOnAttach {
			pd.Note = "wakes on attach"
		}
		out = append(out, pd)
	}
	if len(h.Secrets) > 0 {
		secrets := make([]string, len(h.Secrets))
		for n, s := range h.Secrets {
			secrets[n] = CleanDetail(s)
		}
		out = append(out, Detail{Label: "Secrets", Items: secrets})
	}
	return out
}

// Facts are the lines of what the host said about itself: its system, the
// helper, the agents with their versions, and the limits. They are empty
// when the host has not answered. Each string that the host sent is
// cleaned here, once, for the launcher and `host show` alike.
func (d HostDetails) Facts() []Detail {
	i := d.Info
	if i == nil {
		return nil
	}
	var sys []string
	for _, s := range []string{CleanDetail(i.Distro), CleanDetail(i.Arch), Plural(i.CPUs, "CPU", "CPUs"), SizeText(i.MemTotal), SizeText(i.DiskFree) + " free"} {
		if s != "" && !strings.HasPrefix(s, "0 ") {
			sys = append(sys, s)
		}
	}
	agents := make([]string, 0, len(i.Agents))
	for k := range i.Agents {
		name := CleanDetail(k)
		if v := CleanDetail(i.AgentVersions[k]); v != "" {
			name += " " + v
		}
		if name != "" {
			agents = append(agents, name)
		}
	}
	sort.Strings(agents)
	ad := Detail{Label: "Agents", Items: agents}
	if len(agents) == 0 {
		ad.Value = "none"
	}
	sysLine := Detail{Label: "System", Value: strings.Join(sys, Dot)}
	if d.Tier == TierShared && d.Host.Name != "" {
		sysLine.Note = "reported by " + CleanDetail(d.Host.Name)
	}
	return []Detail{
		sysLine,
		{Label: "Helper", Value: brand.HostBinary + " " + CleanDetail(i.Version)},
		ad,
		{Label: "Limits", Value: SizeText(i.MemoryMax) + " for sessions" + Dot + Plural(d.Sessions, "session", "sessions")},
	}
}

// Plural is n with the word for it: "1 CPU", "4 CPUs".
func Plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// SizeText is a size in binary units: 310 MiB, 6.1 GiB.
func SizeText(b int64) string {
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
