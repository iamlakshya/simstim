package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keys"
	"github.com/iamlakshya/simstim/internal/tui"
)

// The checks of `config check` beyond a value's type: lists, sizes, times,
// keys, launcher commands, secrets and hosts (docs/contributing/cli-spec.md
// 13.2).

// valueChecks are the checks of a key's value, after its type, by file and
// key. A task that adds a key with a list of values, a size, a time or a
// key adds its row.
var valueChecks = map[string]map[string]func(c *checker, v any, p keyPath){
	"config.toml": {
		"ui.motion":              oneOf("auto", "on", "off"), // internal/tui's motionOn
		"attach.scrollback":      oneOf("keep", "clear"),     // internal/client's attach
		"attach.detach_key":      attachKey,
		"attach.image_paste_key": attachKey,
		"sessions.memory_max":    memoryMax,
		"keymap":                 checkKeymap,
		"secret_tiers":           checkSecretTiers,
	},
	"hosts.toml": {
		"host":                    checkHosts,
		"host.machine":            checkMachine,
		"host.machine.provider":   providerName,
		"host.machine.idle_stop":  idleTime,
		"host.machine.busy_lease": leaseTime,
		"host.machine.stdin":      oneOf("send", "stream"), // internal/provider/e2b
	},
}

// checkMachine requires the identity of a provider-managed machine. The
// provider checks its name when present; a missing key needs a finding too.
func checkMachine(c *checker, v any, p keyPath) {
	m, _ := v.(map[string]any)
	if _, found := keyOf(m, "provider"); !found {
		c.add(p, finding{Code: "bad_value", Message: ref(p) + " has no provider"})
	}
	k, found := keyOf(m, "id")
	id, isString := m[k].(string)
	if !found || isString && strings.TrimSpace(id) == "" {
		at := p
		if found {
			at = p.add(k)
		}
		c.add(at, finding{Code: "bad_value", Message: ref(p) + " needs a non-empty id"})
	}
}

// oldKeymapNames maps the old name of a launcher command in [keymap] to
// its new name, until the old name's end (docs/contributing/cli-spec.md
// 3.2). `config check` warns of an old name. The task that renames a
// command adds its row: C11 adds "session: preview".
var oldKeymapNames = map[string]string{"session: preview": "session: peek"}

// oneOf checks a string against its list. "" gives the default.
func oneOf(values ...string) func(*checker, any, keyPath) {
	return func(c *checker, v any, p keyPath) {
		if s, _ := v.(string); s != "" && !slices.Contains(values, s) {
			c.add(p, finding{Code: "bad_value", Message: fmt.Sprintf("%s must be %s, not %q", ref(p), quoted(values), s)})
		}
	}
}

// attachKey checks a key that the client keeps while attached.
func attachKey(c *checker, v any, p keyPath) {
	if s, _ := v.(string); s != "" {
		if _, err := keys.Parse(s); err != nil {
			c.add(p, finding{Code: "bad_value", Message: fmt.Sprintf("%s: %v", ref(p), err)})
		}
	}
}

// memoryMax checks a session's memory cap.
func memoryMax(c *checker, v any, p keyPath) {
	s, _ := v.(string)
	if _, err := api.ParseMemoryMax(s); err != nil {
		c.add(p, finding{Code: "bad_value", Message: fmt.Sprintf("%s: %v. Use auto, or a size such as 4G", ref(p), err)})
	}
}

// idleTime checks how long a machine may idle before it stops, with the
// rule of `host idle`: a minute or more, or never.
func idleTime(c *checker, v any, p keyPath) {
	if s, _ := v.(string); s != "" && client.CheckIdleTime(s) != nil {
		c.add(p, finding{Code: "bad_value", Message: fmt.Sprintf("%s must be a time of a minute or more, such as 30m or 2h, or never, not %q", ref(p), s)})
	}
}

// leaseTime checks how long an E2B sandbox keeps running while a session
// works: a time above zero, or never, as the provider reads it.
func leaseTime(c *checker, v any, p keyPath) {
	if s, _ := v.(string); s != "" && s != "never" {
		if d, err := time.ParseDuration(s); err != nil || d <= 0 {
			c.add(p, finding{Code: "bad_value", Message: fmt.Sprintf("%s must be a time, such as 30m or 2h, or never, not %q", ref(p), s)})
		}
	}
}

// providerName checks the provider of a machine that `host create` made.
func providerName(c *checker, v any, p keyPath) {
	names := make([]string, len(providers))
	for i, pr := range providers {
		names[i] = pr.Name()
	}
	if s, _ := v.(string); !slices.Contains(names, s) {
		c.add(p, finding{Code: "bad_value", Message: fmt.Sprintf("%s must be %s, not %q", ref(p), quoted(names), s)})
	}
}

// checkKeymap checks each name of [keymap] against the launcher's
// commands, and each of its keys.
func checkKeymap(c *checker, v any, p keyPath) {
	m, _ := v.(map[string]any)
	entries := tui.Keymap(client.DefaultSettings())
	commands := make([]named, 0, len(entries))
	known := map[string]bool{}
	for _, e := range entries {
		commands = append(commands, named{e.Command, []string{e.Command}})
		known[e.Command] = true
	}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		here := p.add(name)
		if now, ok := oldKeymapNames[name]; ok {
			c.add(here, finding{Severity: severityWarning, Code: "old_name", Message: fmt.Sprintf("%q is an old name. Use %q", name, now)})
		} else if !known[name] {
			c.add(here, finding{Code: "bad_value", Message: ref(here) + " is not a launcher command", Suggestions: suggest(name, commands, false)})
			continue
		}
		switch x := m[name].(type) {
		case bool:
			// false unbinds the command, and true keeps its keys.
		case string:
			c.launcherKey(here, x)
		case []any:
			for _, k := range x {
				if s, ok := k.(string); ok {
					c.launcherKey(here, s)
				} else {
					c.add(here, finding{Code: "wrong_type", Message: fmt.Sprintf("%s must list keys as strings, not %s", ref(here), valueWords(k))})
				}
			}
		default:
			c.add(here, finding{Code: "wrong_type", Message: fmt.Sprintf("%s must be a key, a list of keys, or \"\" to unbind, not %s", ref(here), valueWords(x))})
		}
	}
}

// launcherKey checks one key of a launcher command. "" unbinds it.
func (c *checker) launcherKey(p keyPath, k string) {
	if k == "" {
		return
	}
	if err := tui.CheckKey(k); err != nil {
		c.add(p, finding{Code: "bad_value", Message: fmt.Sprintf("%s: %v", ref(p), err)})
	}
}

// checkHosts checks that each host has a name that can name a host, that
// no two hosts share one, and that each host that the client reaches with
// SSH has an ssh target. A host that `host create` made has a [machine],
// and its provider reaches it: Docker and E2B without SSH, and Lima
// through the Host that Lima's own SSH config names.
func checkHosts(c *checker, v any, p keyPath) {
	hosts, _ := tablesOf(v)
	seen := map[string]bool{}
	for i, h := range hosts {
		here := p.index(i)
		nameKey, hasName := keyOf(h, "name")
		name, isString := h[nameKey].(string)
		switch {
		case !hasName:
			c.add(here, finding{Code: "bad_host", Message: "a [[host]] has no name"})
		case !isString:
			// A name of the wrong type is a finding already.
		case !client.ValidHostName(name):
			c.add(here.add(nameKey), finding{Code: "bad_host", Message: fmt.Sprintf("%q cannot name a host: use up to 63 letters, digits, dots, dashes and underscores, from a letter or a digit", name)})
		case seen[name]:
			c.add(here.add(nameKey), finding{Code: "bad_host", Message: fmt.Sprintf("two hosts are named %q", name)})
		default:
			seen[name] = true
		}
		if _, made := keyOf(h, "machine"); made {
			continue
		}
		sshKey, hasSSH := keyOf(h, "ssh")
		if s, ok := h[sshKey].(string); !hasSSH || ok && strings.TrimSpace(s) == "" {
			at := here
			if hasSSH {
				at = here.add(sshKey)
			}
			c.add(at, finding{Code: "bad_host", Message: fmt.Sprintf("the host %q has no ssh target", name)})
		}
	}
}

// keyOf is the key of a table that the TOML package matches to the field
// name: the same key, or else one in another case.
func keyOf(t map[string]any, name string) (string, bool) {
	if _, ok := t[name]; ok {
		return name, true
	}
	for _, k := range slices.Sorted(maps.Keys(t)) {
		if strings.EqualFold(k, name) {
			return k, true
		}
	}
	return "", false
}
