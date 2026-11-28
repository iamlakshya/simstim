package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
)

// Fixed completion tables and command-tree wiring for internal/cli/completion.go.

var (
	wallItems = []compItem{
		{api.LevelNone, "No sandbox wall"},
		{api.LevelProcess, "Bubblewrap process wall"},
		{api.LevelGVisor, "gVisor runsc wall"},
	}
	floorItems = []compItem{
		{api.LevelNone, "No sandbox wall"},
		{api.LevelProcess, "Bubblewrap process wall"},
		{api.LevelGVisor, "gVisor runsc wall"},
		{api.LevelMicroVM, "Hardware-virtualised microVM wall"},
	}
	hostSetKeys = []compItem{
		{"floor=", "Weakest sandbox level for new sessions"},
		{"idle=", "Idle stop time for a created host"},
		{"label.", "Host label (label.<key>=<value>)"},
		{"trusted=", "Whether you control root on this host"},
	}
	trustedItems = []compItem{
		{"true", "You control root on this host"},
		{"false", "Untrusted host (default)"},
	}
	autostartItems = []compItem{
		{"boot", "Start the VM at boot"},
		{"login", "Start the VM at login"},
		{"off", "Do not start the VM automatically"},
	}
	alertFormatItems = []compItem{
		{"json", "Raw event JSON"},
		{"slack", "Slack incoming webhook"},
		{"discord", "Discord webhook"},
	}
	alertEventItems = []compItem{
		{"needs_input", "Agent is waiting for input"},
		{"turn_done", "Agent finished a turn"},
		{"exited", "Agent exited"},
		{"failed", "Agent failed"},
		{"oom", "Session hit its memory cap"},
		{"memory", "Session memory warning"},
		{"started", "Session started"},
		{"restored", "Session restored after restart"},
	}
	profileItems = []compItem{
		{diag.ProfileCPU, "CPU profile"},
		{diag.ProfileHeap, "Heap memory profile"},
		{diag.ProfileGoroutine, "Goroutine stack profile"},
	}
)

type argCompletionBinding struct {
	path []string
	fn   cobra.CompletionFunc
}

type flagCompletionBinding struct {
	path []string
	flag string
	fn   cobra.CompletionFunc
}

// wireCompletions attaches argument and flag value completion functions across
// the command tree.
func wireCompletions(root *cobra.Command) {
	for _, b := range argCompletionBindings() {
		if c := findPath(root, b.path...); c != nil {
			c.ValidArgsFunction = b.fn
		}
	}
	for _, b := range flagCompletionBindings() {
		if c := findPath(root, b.path...); c != nil {
			_ = c.RegisterFlagCompletionFunc(b.flag, b.fn)
		}
	}
}

// verifyCompletionBindings checks that every argument and flag binding resolves
// on root and has a completion function registered, catching renames in tests.
func verifyCompletionBindings(root *cobra.Command) []string {
	var missing []string
	for _, b := range argCompletionBindings() {
		c := findPath(root, b.path...)
		if c == nil || c.ValidArgsFunction == nil {
			missing = append(missing, "command "+strings.Join(b.path, " "))
		}
	}
	for _, b := range flagCompletionBindings() {
		c := findPath(root, b.path...)
		if c == nil {
			missing = append(missing, "command "+strings.Join(b.path, " "))
			continue
		}
		if _, ok := c.GetFlagCompletionFunc(b.flag); !ok {
			missing = append(missing, "flag "+strings.Join(b.path, " ")+" --"+b.flag)
		}
	}
	return missing
}

func argCompletionBindings() []argCompletionBinding {
	var out []argCompletionBinding
	add := func(fn cobra.CompletionFunc, paths ...[]string) {
		for _, p := range paths {
			out = append(out, argCompletionBinding{path: p, fn: fn})
		}
	}

	add(completeHostAt(0),
		[]string{"new"},
		[]string{"ls"},
		[]string{"host", "show"},
		[]string{"host", "upgrade"},
		[]string{"host", "rm"},
		[]string{"host", "rekey"},
		[]string{"host", "test"},
		[]string{"host", "doctor"},
		[]string{"host", "restore"},
		[]string{"host", "alerts"},
		[]string{"host", "alerts", "set"},
		[]string{"host", "alerts", "test"},
		[]string{"host", "alerts", "log"},
		[]string{"host", "alerts", "off"},
		[]string{"host", "start"},
		[]string{"host", "stop"},
		[]string{"host", "status"},
		[]string{"image", "ls"},
	)

	add(completeSessionAt(0),
		[]string{"attach"},
		[]string{"show"},
		[]string{"rm"},
		[]string{"resume"},
		[]string{"stop"},
		[]string{"rename"},
		[]string{"send"},
		[]string{"ports"},
		[]string{"forward"},
		[]string{"pull"},
		[]string{"debug", "start"},
		[]string{"debug", "stop"},
		[]string{"debug", "save"},
	)

	add(cobra.NoFileCompletions,
		[]string{"setup"},
		[]string{"host"},
		[]string{"host", "add"},
		[]string{"host", "ls"},
		[]string{"agents"},
		[]string{"agents", "ls"},
		[]string{"image"},
		[]string{"doctor"},
		[]string{"doctor", "terminal"},
		[]string{"doctor", "keys"},
		[]string{"debug"},
		[]string{"config"},
		[]string{"config", "check"},
		[]string{"config", "keymap"},
		[]string{"config", "path"},
		[]string{"ssh-config"},
		[]string{"demo"},
		[]string{"version"},
	)

	out = append(out,
		argCompletionBinding{path: []string{"host", "set"}, fn: completeHostSet},
		argCompletionBinding{path: []string{"host", "create"}, fn: completeCreateArgs},
		argCompletionBinding{path: []string{"agents", "install"}, fn: completeAgentAndHost(completeInstallableAgents)},
		argCompletionBinding{path: []string{"agents", "login"}, fn: completeAgentAndHost(completeLoginAgents)},
		argCompletionBinding{path: []string{"image", "pull"}, fn: completeImagePullArgs},
		argCompletionBinding{path: []string{"image", "rm"}, fn: completeHostAt(1)},
		argCompletionBinding{path: []string{"debug", "profile"}, fn: completeDebugProfileArgs},
		argCompletionBinding{path: []string{"debug", "bundle"}, fn: completeDebugBundleArgs},
		argCompletionBinding{path: []string{"cp"}, fn: completeCPArgs},
	)
	return out
}

func flagCompletionBindings() []flagCompletionBinding {
	return []flagCompletionBinding{
		{path: []string{"ls"}, flag: "host", fn: completeHosts},
		{path: []string{"agents", "install"}, flag: "host", fn: completeHosts},
		{path: []string{"agents", "login"}, flag: "host", fn: completeHosts},
		{path: []string{"setup"}, flag: "host", fn: completeHosts},
		{path: []string{"host", "set"}, flag: "confirm", fn: completeConfirmHost},
		{path: []string{"host", "rm"}, flag: "confirm", fn: completeConfirmHost},
		{path: []string{"host", "rekey"}, flag: "confirm", fn: completeConfirmHost},
		{path: []string{"new"}, flag: "agent", fn: completeAgents},
		{path: []string{"new"}, flag: "wall", fn: completeFixed(wallItems)},
		{path: []string{"new"}, flag: "secret", fn: completeSecrets},
		{path: []string{"new"}, flag: "expose", fn: completeSecrets},
		{path: []string{"resume"}, flag: "secret", fn: completeSecrets},
		{path: []string{"setup"}, flag: "create", fn: completeProviders},
		{path: []string{"setup"}, flag: "agents", fn: completeAgentsList},
		{path: []string{"host", "add"}, flag: "floor", fn: completeFixed(floorItems)},
		{path: []string{"host", "add"}, flag: "autostart", fn: completeFixed(autostartItems)},
		{path: []string{"host", "create"}, flag: "floor", fn: completeFixed(floorItems)},
		{path: []string{"host", "create"}, flag: "autostart", fn: completeFixed(autostartItems)},
		{path: []string{"host", "alerts", "set"}, flag: "format", fn: completeFixed(alertFormatItems)},
		{path: []string{"host", "alerts", "set"}, flag: "events", fn: completeAlertEvents},
		{path: []string{"host", "alerts", "test"}, flag: "event", fn: completeAlertTestEvent},
	}
}

// findPath walks subcommands by exact name without executing aliases or errors.
func findPath(root *cobra.Command, names ...string) *cobra.Command {
	cur := root
	for _, name := range names {
		var next *cobra.Command
		for _, sub := range cur.Commands() {
			if sub.Name() == name {
				next = sub
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}
