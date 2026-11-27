package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Shell completion (docs/contributing/cli-spec.md, section 14). Every
// completion reads local files only, never dials a host or runs a command,
// never waits on a lock, never writes the session cache, and cleans every
// value and description that came from a host or a file. The bounded file
// readers and text cleaners are in completion_read.go.

// compNow is the clock of session completion, for "seen 5m ago". Tests set it.
var compNow = time.Now

// isCompletionRequest reports whether cmd is Cobra's hidden completion
// command (__complete or __completeNoDesc).
func isCompletionRequest(cmd *cobra.Command) bool {
	return cmd != nil && (cmd.Name() == cobra.ShellCompRequestCmd || cmd.Name() == cobra.ShellCompNoDescRequestCmd)
}

// wireCompletion sets the default directive, updates the help of Cobra's
// completion commands, and registers flag completers across root.
func wireCompletion(root *cobra.Command) {
	noFile := cobra.ShellCompDirectiveNoFileComp
	root.CompletionOptions.DefaultShellCompDirective = &noFile
	polishCompletionCmd(root)
	wireKnownFlagCompletions(root)
	wireQuietCompletion(root)
}

func polishCompletionCmd(root *cobra.Command) {
	comp, _, err := root.Find([]string{"completion"})
	if err != nil || comp == nil || comp == root {
		return
	}
	comp.Short = "Print the completion script for bash, zsh, fish or PowerShell"
	comp.Long = "Completion fills in commands, flags, saved hosts, cached sessions and agents from local files.\n" +
		"It never connects to a host. See docs/how-to/shell-completion.md."
	see := "\nSee docs/how-to/shell-completion.md."
	for _, sub := range comp.Commands() {
		if f := sub.Flags().Lookup("no-descriptions"); f != nil {
			f.Usage = "leave out completion descriptions"
		}
		switch sub.Name() {
		case "bash":
			sub.DisableFlagsInUseLine = false
			sub.Short = "Print the completion script for bash"
			sub.Long = "This script needs the `bash-completion` package. Load it in your shell, or install it once." + see
			sub.Example = "  source <(" + brand.Name + " completion bash)"
		case "zsh":
			sub.Short = "Print the completion script for zsh"
			sub.Long = "Put the output in a folder on your `fpath` as `_" + brand.Name + "`, or load it in `~/.zshrc`." + see
			sub.Example = "  " + brand.Name + " completion zsh > \"${fpath[1]}/_" + brand.Name + "\""
		case "fish":
			sub.Short = "Print the completion script for fish"
			sub.Long = "Save the output in `~/.config/fish/completions/" + brand.Name + ".fish`, or pipe it to `source`." + see
			sub.Example = "  " + brand.Name + " completion fish > ~/.config/fish/completions/" + brand.Name + ".fish"
		case "powershell":
			sub.Short = "Print the completion script for PowerShell"
			sub.Long = "Load the output in your PowerShell session, or add that line to your PowerShell profile." + see
			sub.Example = "  " + brand.Name + " completion powershell | Out-String | Invoke-Expression"
		}
	}
}

func wireKnownFlagCompletions(root *cobra.Command) {
	alertEvents := slices.Clone(alerts.Kinds)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if !c.Hidden {
			registerKnownFlags(c, c.NonInheritedFlags(), alertEvents)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	registerKnownFlags(root, root.PersistentFlags(), alertEvents)
	walk(root)
}

func registerKnownFlags(c *cobra.Command, fs *pflag.FlagSet, alertEvents []string) {
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Deprecated != "" {
			return
		}
		var fn cobra.CompletionFunc
		switch f.Name {
		case "host":
			fn = completeHosts
		case "in":
			fn = completeInFlag
		case "agent":
			fn = completeAgents
		case "agents":
			fn = completeAgentList
		case "create":
			fn = completeProviders
		case "secret":
			fn = completeSecrets
		case "tools":
			fn = completeCommaFixed(api.ModelToolNames()...)
		case "profile":
			fn = completeProfiles
		case "wall":
			fn = completeFixed("auto", "none", "process", "gvisor", "microvm")
		case "tier":
			fn = completeFixed("own", "throwaway", "shared")
		case "preset":
			fn = completeFixed("locked", "balanced", "open")
		case "root-step":
			fn = completeFixed("run", "print", "skip")
		case "images":
			fn = completeFixed("auto", "host", "laptop")
		case "memory-max":
			fn = completeFixed("auto")
		case "idle-stop":
			fn = completeFixed("never")
		case "format":
			fn = completeFixed("json", "slack", "discord", "otlp")
		case "events":
			fn = completeCommaFixed(alertEvents...)
		case "event":
			fn = completeFixed(append([]string{alerts.Test}, alertEvents...)...)
		case "debug-file", "ssh-config", "provider-script", "env-file", "seed":
			fn = completeLocalFiles
		}
		if fn != nil {
			_ = c.RegisterFlagCompletionFunc(f.Name, fn)
		}
	})
}

func firstArg(fn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return fn(cmd, args, toComplete)
	}
}

var (
	completeHostArg    = firstArg(completeHosts)
	completeSessionArg = firstArg(completeSessions)
	completeAgents     = completeAgentSlice(agents.Table)
)

func completeLsHost(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 || cmd.Flags().Changed("host") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeHosts(cmd, args, toComplete)
}

func completeNewHost(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 || completionAtDash(cmd) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeHosts(cmd, args, toComplete)
}

func completeAgentCmdArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeAgentSlice(agents.Installable())(cmd, args, toComplete)
	}
	return completeAgentHost(cmd, args, toComplete)
}

func completeLoginCmdArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeFixed(loginKinds()...)(cmd, args, toComplete)
	}
	return completeAgentHost(cmd, args, toComplete)
}

func completeAgentHost(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	hosts, directive := completeHosts(cmd, args, toComplete)
	if host, _ := cmd.Flags().GetString("host"); host != "" {
		hosts = slices.DeleteFunc(hosts, func(item string) bool {
			name, _, _ := strings.Cut(item, "\t")
			return name != host
		})
	}
	return hosts, directive
}

func completeHostOrSessionArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if strings.Contains(toComplete, "/") {
		return completeSessions(cmd, args, toComplete)
	}
	hosts, _ := completeHosts(cmd, args, toComplete)
	sessions, _ := completeSessions(cmd, args, toComplete)
	return append(hosts, sessions...), cobra.ShellCompDirectiveNoFileComp
}

func completeHostSetArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeHosts(cmd, args, toComplete)
	}
	return completeHostSettings(hostSettings)(cmd, args, toComplete)
}

// completeHostSettings completes key=value pairs of host set from settings.
func completeHostSettings(settings []hostSetting) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if key, val, ok := strings.Cut(toComplete, "="); ok {
			return completeSettingValues(settings, key, val), cobra.ShellCompDirectiveNoFileComp
		}
		return completeSettingKeys(settings, args[1:], toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
}

func completeSettingValues(settings []hostSetting, key, val string) []string {
	for _, s := range settings {
		if s.prefix || s.key != key {
			continue
		}
		var out []string
		for _, v := range s.values {
			if strings.HasPrefix(v, val) {
				out = append(out, key+"="+v)
			}
		}
		return out
	}
	return nil
}

func completeSettingKeys(settings []hostSetting, given []string, toComplete string) []string {
	used := map[string]bool{}
	for _, w := range given {
		if k, _, ok := strings.Cut(w, "="); ok {
			used[k] = true
		}
	}
	var out []string
	for _, s := range settings {
		cand := s.key + "="
		if s.prefix {
			cand = s.key
		} else if used[s.key] {
			continue
		}
		if strings.HasPrefix(cand, toComplete) {
			out = append(out, cand)
		}
	}
	return out
}

func completeLocalFiles(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveDefault
}

func completeFixed(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var out []string
		for _, v := range values {
			if strings.HasPrefix(v, toComplete) {
				out = append(out, v)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeCommaFixed(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		prefix := toComplete[:strings.LastIndex(toComplete, ",")+1]
		used := map[string]bool{}
		for _, p := range strings.Split(strings.TrimSuffix(prefix, ","), ",") {
			if p != "" {
				used[p] = true
			}
		}
		var out []string
		for _, v := range values {
			if cand := prefix + v; !used[v] && strings.HasPrefix(cand, toComplete) {
				out = append(out, cand)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
}

func completeProviders(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, p := range providers {
		if strings.HasPrefix(p.Name(), toComplete) {
			out = append(out, p.Name())
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAgentSlice(list []agents.Agent) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var out []string
		for _, a := range list {
			if k := cleanCompValue(a.Kind); k != "" && strings.HasPrefix(k, toComplete) {
				out = append(out, compItem(k, cleanCompDesc(a.Title)))
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeAgentList(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	items := installable()
	if !strings.Contains(toComplete, ",") {
		var out []string
		for _, k := range append(items, "none") {
			if strings.HasPrefix(k, toComplete) {
				out = append(out, k)
			}
		}
		directive := cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		if len(out) == 1 && out[0] == "none" {
			directive = cobra.ShellCompDirectiveNoFileComp
		}
		return out, directive
	}
	for _, item := range strings.Split(toComplete, ",") {
		if item == "none" {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}
	return completeCommaFixed(items...)(nil, nil, toComplete)
}

func completeHosts(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	hosts, ok := loadCompletionHosts(completionContext(cmd))
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, h := range hosts {
		if strings.HasPrefix(h.Name, toComplete) {
			out = append(out, compItem(h.Name, cleanCompDesc(h.Target())))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeSessions(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cache, ok := loadCompletionCache(completionContext(cmd))
	if !ok || len(cache.Sessions) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	now := compNow()
	var out []string
	for _, hn := range completionHostNames(cache, completionContext(cmd)) {
		if completionContext(cmd).Err() != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out = appendHostSessions(out, hn, cache.Sessions[hn], cache.Seen[hn], now, toComplete)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completionHostNames(cache client.SessionCache, contexts ...context.Context) []string {
	if completionReadContext(contexts).Err() != nil {
		return nil
	}
	if hosts, ok := loadCompletionHosts(contexts...); ok {
		names := make([]string, len(hosts))
		for i, h := range hosts {
			names[i] = h.Name
		}
		return names
	}
	if completionReadContext(contexts).Err() != nil {
		return nil
	}
	var names []string
	for hn := range cache.Sessions {
		if client.ValidHostName(hn) && cleanCompValue(hn) == hn {
			names = append(names, hn)
		}
	}
	slices.Sort(names)
	return names
}

func appendHostSessions(out []string, hn string, list []client.Session, seen, now time.Time, toComplete string) []string {
	if len(list) == 0 {
		return out
	}
	counts := make(map[string]int, len(list))
	ids := make(map[string]bool, len(list))
	validIDs := make([]string, len(list))
	names := make([]string, len(list))
	for i, s := range list {
		id := cleanCompValue(s.ID)
		if id != "" && id == s.ID && strings.TrimSpace(id) == id && !strings.Contains(id, "/") {
			validIDs[i], ids[id] = id, true
		}
	}
	for i, s := range list {
		n := s.Name
		if validIDs[i] != "" && n != "" && cleanCompValue(n) == n && strings.TrimSpace(n) == n && !api.LooksLikeID(n) && !ids[n] {
			names[i], counts[n] = n, counts[n]+1
		}
	}
	stale := ""
	if !seen.IsZero() && now.Sub(seen) > staleAfter {
		stale = "seen " + seenAge(now.Sub(seen)) + " ago"
	}
	for i, s := range list {
		if validIDs[i] == "" {
			continue
		}
		ref := names[i]
		if ref == "" || counts[ref] > 1 {
			ref = validIDs[i]
		}
		if ref != "" && strings.HasPrefix(hn+"/"+ref, toComplete) {
			out = append(out, compItem(hn+"/"+ref, sessionCompDesc(s, stale)))
		}
	}
	return out
}

func sessionCompDesc(s client.Session, stale string) string {
	// The common state formatter cleans control bytes. Remove complete
	// escape sequences first so their printable tails cannot survive it.
	s.State, s.Signal, s.Reason = stripEscapes(s.State), stripEscapes(s.Signal), stripEscapes(s.Reason)
	var parts []string
	for _, p := range []string{cleanCompDesc(s.Agent), cleanCompDesc(stateLabel(s)), stale} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func seenAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 1))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func completeSecrets(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeConfigMapKeys("secrets", toComplete, completionContext(cmd)), cobra.ShellCompDirectiveNoFileComp
}

func completeProfiles(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	metadata, ok := completionPolicyMetadata(false, completionContext(cmd))
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completionProfileNames(metadata.Profiles, toComplete), cobra.ShellCompDirectiveNoFileComp
}
