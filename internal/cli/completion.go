package cli

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// Shell completion of values (docs/contributing/cli-spec.md 14).
//
// Every completer reads local files only (hosts.toml, launcher.json and
// config.toml) and never dials a host, runs a provider script, opens the
// network or writes the session cache. A missing or broken file yields no
// candidates with ShellCompDirectiveNoFileComp, never ShellCompDirectiveError.

// completionNow is the clock used for stale-cache descriptions. Tests set it.
var completionNow = time.Now

type compItem struct {
	val, desc string
}

func completeHostAt(pos int) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != pos {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return completeHosts(cmd, args, toComplete)
	}
}

func completeSessionAt(pos int) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != pos {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return completeSessions(cmd, args, toComplete)
	}
}

func completeHosts(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	hosts, err := client.LoadHosts()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	slices.SortFunc(hosts, func(a, b client.Host) int {
		return strings.Compare(a.Name, b.Name)
	})
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		name := cleanCompToken(h.Name)
		if name == "" || !strings.HasPrefix(name, toComplete) {
			continue
		}
		out = append(out, compEntry(name, cleanCompDesc(h.Target())))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeConfirmHost(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		name := cleanCompToken(args[0])
		if name != "" && strings.HasPrefix(name, toComplete) {
			return []string{name}, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeHosts(cmd, args, toComplete)
}

func completeSessions(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return cachedSessionCompletions(toComplete, ""), cobra.ShellCompDirectiveNoFileComp
}

// cachedSessionCompletions returns cached sessions formatted as
// "<host>/<name><suffix>\t<desc>", falling back to "<host>/<id>" when a session
// has no usable name or shares a name with another session on the same host.
func cachedSessionCompletions(toComplete, suffix string) []string {
	cache := client.LoadSessionCache()
	if len(cache.Sessions) == 0 {
		return nil
	}
	hostNames := make([]string, 0, len(cache.Sessions))
	total := 0
	for h, list := range cache.Sessions {
		if len(list) == 0 {
			continue
		}
		hostNames = append(hostNames, h)
		total += len(list)
	}
	if total == 0 {
		return nil
	}
	sort.Strings(hostNames)
	now := completionNow()
	out := make([]string, 0, total)
	for _, host := range hostNames {
		cleanHost := cleanCompToken(host)
		if cleanHost == "" {
			continue
		}
		if slash := strings.IndexByte(toComplete, '/'); slash >= 0 && cleanHost != toComplete[:slash] {
			continue
		}
		list := cache.Sessions[host]
		counts := make(map[string]int, len(list))
		for _, s := range list {
			if s.Name != "" {
				counts[s.Name]++
			}
		}
		seenSuffix := ""
		if seen := cache.Seen[host]; !seen.IsZero() && now.Sub(seen) > staleAfter {
			seenSuffix = "seen " + ageAt(now, seen) + " ago"
		}
		items := make([]compItem, 0, len(list))
		for _, s := range list {
			ref := sessionCompRef(s, counts)
			if ref == "" {
				continue
			}
			val := cleanHost + "/" + ref + suffix
			if toComplete != "" && !strings.HasPrefix(val, toComplete) {
				continue
			}
			items = append(items, compItem{val: val, desc: sessionCompDesc(s, seenSuffix)})
		}
		slices.SortFunc(items, func(a, b compItem) int {
			return strings.Compare(a.val, b.val)
		})
		for _, it := range items {
			out = append(out, compEntry(it.val, it.desc))
		}
	}
	return out
}

func sessionCompRef(s client.Session, counts map[string]int) string {
	if s.Name != "" && counts[s.Name] == 1 {
		name := cleanCompToken(s.Name)
		if name != "" && name == s.Name && !api.LooksLikeID(name) && !strings.ContainsAny(name, "/ \t\r\n") {
			return name
		}
	}
	return cleanCompToken(s.ID)
}

func sessionCompDesc(s client.Session, seenSuffix string) string {
	var parts [3]string
	n := 0
	if a := cleanCompDesc(s.Agent); a != "" {
		parts[n] = a
		n++
	}
	if st := cleanCompDesc(stateLabel(s)); st != "" {
		parts[n] = st
		n++
	}
	if seenSuffix != "" {
		parts[n] = seenSuffix
		n++
	}
	return strings.Join(parts[:n], ", ")
}

func completeAgents(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAgentSlice(agents.Table, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeInstallableAgents(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAgentSlice(agents.Installable(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeLoginAgents(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var list []agents.Agent
	for _, a := range agents.Table {
		if a.Setup != nil && a.Setup.Login != "" {
			list = append(list, a)
		}
	}
	return completeAgentSlice(list, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeAgentSlice(list []agents.Agent, toComplete string) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		if strings.HasPrefix(a.Kind, toComplete) {
			out = append(out, compEntry(a.Kind, cleanCompDesc(a.Title)))
		}
	}
	return out
}

func completeAgentAndHost(agentFn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 0:
			return agentFn(cmd, args, toComplete)
		case 1:
			return completeHosts(cmd, args, toComplete)
		default:
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}
}

func completeAgentsList(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	prefix := toComplete[:strings.LastIndex(toComplete, ",")+1]
	seen := map[string]bool{}
	for _, part := range strings.Split(prefix, ",") {
		if p := strings.TrimSpace(part); p != "" {
			seen[p] = true
		}
	}
	if seen["none"] {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	installable := agents.Installable()
	items := make([]compItem, 0, len(installable)+1)
	for _, a := range installable {
		items = append(items, compItem{val: a.Kind, desc: a.Title})
	}
	if prefix == "" {
		items = append(items, compItem{val: "none", desc: "Install no agents"})
	}
	return completeCommaItems(items, prefix, seen, toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

func completeHostSet(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeHosts(cmd, args, toComplete)
	}
	key, _, hasEq := strings.Cut(toComplete, "=")
	if !hasEq {
		return matchItems(hostSetKeys, toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
	switch key {
	case "idle":
		items := []compItem{{val: "idle=never", desc: "Never stop when idle"}}
		return matchItems(items, toComplete), cobra.ShellCompDirectiveNoFileComp
	case "floor":
		return matchPrefixedItems("floor=", floorItems, toComplete), cobra.ShellCompDirectiveNoFileComp
	case "trusted":
		return matchPrefixedItems("trusted=", trustedItems, toComplete), cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeProviders(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		name := p.Name()
		if strings.HasPrefix(name, toComplete) {
			out = append(out, name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeCreateArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeProviders(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeImagePullArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		items := []compItem{{val: "default", desc: "Pinned official buildpack-deps:trixie-scm image"}}
		return matchItems(items, toComplete), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return completeHosts(cmd, args, toComplete)
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeDebugProfileArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeSessions(cmd, args, toComplete)
	case 1:
		return matchItems(profileItems, toComplete), cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeDebugBundleArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	hosts, _ := completeHosts(cmd, args, toComplete)
	sessions := cachedSessionCompletions(toComplete, "")
	out := make([]string, 0, len(hosts)+len(sessions))
	out = append(out, hosts...)
	out = append(out, sessions...)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeCPArgs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		if isLocalCPPrefix(toComplete) {
			return nil, cobra.ShellCompDirectiveDefault
		}
		return cachedSessionCompletions(toComplete, ":"), cobra.ShellCompDirectiveNoSpace
	case 1:
		_, _, _, srcRemote := parseCPSpec(args[0])
		if srcRemote {
			return nil, cobra.ShellCompDirectiveDefault
		}
		return cachedSessionCompletions(toComplete, ":"), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func isLocalCPPrefix(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~")
}

func completeSecrets(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	s := client.LoadSettings()
	if len(s.Secrets) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(s.Secrets))
	for k := range s.Secrets {
		if clean := cleanCompToken(k); clean != "" && strings.HasPrefix(clean, toComplete) {
			names = append(names, clean)
		}
	}
	sort.Strings(names)
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeAlertEvents(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	prefix := toComplete[:strings.LastIndex(toComplete, ",")+1]
	seen := map[string]bool{}
	for _, part := range strings.Split(prefix, ",") {
		if p := strings.TrimSpace(part); p != "" {
			seen[p] = true
		}
	}
	return completeCommaItems(alertEventItems, prefix, seen, toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

func completeAlertTestEvent(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	items := make([]compItem, 0, len(alertEventItems)+1)
	items = append(items, compItem{val: "test", desc: "Test alert"})
	items = append(items, alertEventItems...)
	return matchItems(items, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeFixed(items []compItem) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return matchItems(items, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

func matchItems(items []compItem, toComplete string) []string {
	var out []string
	for _, it := range items {
		if strings.HasPrefix(it.val, toComplete) {
			out = append(out, compEntry(it.val, it.desc))
		}
	}
	return out
}

func matchPrefixedItems(prefix string, items []compItem, toComplete string) []string {
	var out []string
	for _, it := range items {
		cand := prefix + it.val
		if strings.HasPrefix(cand, toComplete) {
			out = append(out, compEntry(cand, it.desc))
		}
	}
	return out
}

func completeCommaItems(items []compItem, prefix string, seen map[string]bool, toComplete string) []string {
	var out []string
	for _, it := range items {
		if seen[it.val] {
			continue
		}
		cand := prefix + it.val
		if strings.HasPrefix(cand, toComplete) {
			out = append(out, compEntry(cand, it.desc))
		}
	}
	return out
}

func compEntry(val, desc string) string {
	if desc == "" {
		return val
	}
	return val + "\t" + desc
}

func cleanCompToken(s string) string { return strings.TrimSpace(shownShort(s)) }
func cleanCompDesc(s string) string  { return strings.TrimSpace(shownShort(s)) }

func ageAt(now, t time.Time) string {
	d := max(now.Sub(t), 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
