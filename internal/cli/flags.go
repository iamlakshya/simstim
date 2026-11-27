package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/api"
)

// One meaning for each flag (docs/contributing/cli-spec.md 10). The flags
// that more than one command takes come from the helpers of this file, so
// each means one thing everywhere, and the values of every flag follow the
// same rules: a list takes a repeated flag or commas, a size takes a unit,
// and a time is a Go duration.

// sharedKey marks a flag that a helper of this file defined. Its value is
// the flag's name. A test fails on a flag of cli-spec 10.2 without it.
const sharedKey = "shared_flag"

func shared(fs *pflag.FlagSet, name string) {
	_ = fs.SetAnnotation(name, sharedKey, []string{name})
}

// addYes adds -y/--yes, which answers yes to every question that the
// command would ask, and does nothing more: what says what it answers.
func addYes(cmd *cobra.Command, p *bool, what string) {
	cmd.Flags().BoolVarP(p, "yes", "y", false, "answer yes to every question: "+what)
	shared(cmd.Flags(), "yes")
}

// addForce adds --force, which goes on although work is lost: what says
// which work.
func addForce(cmd *cobra.Command, p *bool, what string) {
	cmd.Flags().BoolVar(p, "force", false, "go on although work is lost: "+what)
	shared(cmd.Flags(), "force")
}

// addName adds -n/--name, which names what the command makes.
func addName(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVarP(p, "name", "n", "", usage)
	shared(cmd.Flags(), "name")
}

// addHostFlag adds --host, the host to act on.
func addHostFlag(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "host", "", usage)
	shared(cmd.Flags(), "host")
}

// addDetach adds --detach, which starts without attaching. Its short form
// is -d only after the end of the old form "new -d" (cli-spec 10.4).
func addDetach(cmd *cobra.Command, p *bool, usage string) {
	short := "d"
	if liveOld("new -d") {
		short = ""
	}
	cmd.Flags().BoolVarP(p, "detach", short, false, usage)
	shared(cmd.Flags(), "detach")
}

// addCount adds the number of independent sessions to start.
func addCount(cmd *cobra.Command, p *int, usage string) {
	cmd.Flags().IntVar(p, "count", 1, usage)
	shared(cmd.Flags(), "count")
}

// addMemoryMax adds --memory-max, the memory cap of one session.
func addMemoryMax(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "memory-max", "", usage)
	shared(cmd.Flags(), "memory-max")
}

// addMemory adds --memory, the memory that a machine gets.
func addMemory(cmd *cobra.Command, v *sizeValue, usage string) {
	addSize(cmd.Flags(), v, "memory", usage)
	shared(cmd.Flags(), "memory")
}

// addCPUs adds --cpus, the CPUs that a machine gets.
func addCPUs(cmd *cobra.Command, p *int, def int, usage string) {
	cmd.Flags().IntVar(p, "cpus", def, usage)
	shared(cmd.Flags(), "cpus")
}

// addSecret adds --secret, a list of the names of secrets from config.toml.
func addSecret(cmd *cobra.Command, p *[]string, usage string) {
	addList(cmd.Flags(), p, "secret", usage)
	shared(cmd.Flags(), "secret")
}

// addFor adds --for, how long something lasts.
func addFor(cmd *cobra.Command, p *time.Duration, def time.Duration, usage string) {
	cmd.Flags().DurationVar(p, "for", def, usage)
	shared(cmd.Flags(), "for")
}

// addAll adds --all, which widens the command to every session.
func addAll(cmd *cobra.Command, p *bool, usage string) {
	cmd.Flags().BoolVar(p, "all", false, usage)
	shared(cmd.Flags(), "all")
}

// addFix adds --fix, which offers each fix.
func addFix(cmd *cobra.Command, p *bool) {
	cmd.Flags().BoolVar(p, "fix", false, "offer to fix the problems found, one by one")
	shared(cmd.Flags(), "fix")
}

// addLabel adds --label, a list of the host's labels as key=value.
func addLabel(cmd *cobra.Command, p *[]string, usage string) {
	addList(cmd.Flags(), p, "label", usage)
	shared(cmd.Flags(), "label")
}

// addTokenCommand adds --token-command, a command that prints a token on
// this computer.
func addTokenCommand(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "token-command", "", usage)
	shared(cmd.Flags(), "token-command")
}

// addRootStep adds --root-step, how to handle optional root changes.
func addRootStep(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "root-step", "", usage)
	shared(cmd.Flags(), "root-step")
}

// addImages adds --images, where runtime files and images come from.
func addImages(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "images", "", usage)
	shared(cmd.Flags(), "images")
}

// listValue is a list of names (cli-spec 10.3). The flag may come again,
// and each value may hold several names, split at its commas, so
// "--agents claude,codex" is "--agents claude --agents codex". Spaces
// around a name do not count, and an empty name is dropped. A list of
// values that may hold a comma, such as paths, takes pflag's StringArray
// instead.
type listValue struct {
	list    *[]string
	changed bool
	// typed is true when a use of the flag held any text, even only a
	// comma. An empty value, as "", leaves it false.
	typed bool
}

// addList adds the list flag name to fs.
func addList(fs *pflag.FlagSet, p *[]string, name, usage string) {
	fs.Var(&listValue{list: p}, name, usage)
}

// splitList splits one value of a list flag into its names.
func splitList(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Set adds the names of one use of the flag. The first use replaces the
// default.
func (l *listValue) Set(s string) error {
	if !l.changed {
		*l.list, l.changed = nil, true
	}
	l.typed = l.typed || strings.TrimSpace(s) != ""
	*l.list = append(*l.list, splitList(s)...)
	return nil
}

// listTyped reports whether the list flag name of fs got any text, so that
// a command can tell an empty value from a list of no names.
func listTyped(fs *pflag.FlagSet, name string) bool {
	l, ok := fs.Lookup(name).Value.(*listValue)
	return ok && l.typed
}

// Type is the name that help shows for the value. It is not pflag's
// "stringSlice": pflag's GetStringSlice would read String() in its own
// form. A list's names come from GetSlice, and shell completion offers the
// flag again, as for any pflag.SliceValue.
func (*listValue) Type() string { return "list" }

// String returns the names separated by commas.
func (l *listValue) String() string { return strings.Join(*l.list, ",") }

// Append adds the names in one comma-separated value.
func (l *listValue) Append(s string) error {
	*l.list = append(*l.list, splitList(s)...)
	return nil
}

// Replace replaces the list with the names in each value.
func (l *listValue) Replace(s []string) error {
	*l.list = nil
	for _, v := range s {
		*l.list = append(*l.list, splitList(v)...)
	}
	return nil
}

// GetSlice returns a copy of the names.
func (l *listValue) GetSlice() []string { return slices.Clone(*l.list) }

// listItem is the number of names in one value of a list flag, and the
// index among its comma-separated parts of its name at index item.
func listItem(value string, item int) (count, at int) {
	at = -1
	for i, part := range strings.Split(value, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		if count == item {
			at = i
		}
		count++
	}
	return count, at
}

// parseSize reads a size (cli-spec 10.3): a number and a unit in powers of
// 1024, such as 512M, 4G, 4GiB or 1.5G, with the unit rules of
// api.ParseSize. A size that comes to less than one byte is refused. With
// bareGiB, a bare number is a number of GiB, as an old form still takes
// it, and bare is true. Such a number of 0 or less is the default, as it
// was: the size is 0.
func parseSize(s string, bareGiB bool) (bytes int64, bare bool, err error) {
	t := strings.TrimSpace(s)
	if bareGiB {
		if withUnit, ok := oldSize(t); ok {
			if withUnit == "" {
				return 0, true, nil
			}
			t, bare = withUnit, true
		}
	}
	n, err := api.ParseSize(t)
	if err != nil {
		return 0, false, err
	}
	if n < 1 {
		return 0, false, fmt.Errorf("%q is not a size", s)
	}
	return n, bare, nil
}

// oldSize reads t as a bare number of GiB, as the old size flags took it,
// and returns the same size with its unit, such as "8G". The old flags were
// pflag's int flags, which read a whole number in any of Go's forms, as
// strconv.ParseInt does with base 0: 8, +8, 010 (8), 0x10 (16) or 0b11
// (3). A bare number with a fraction, such as 1.5, is GiB too. A number of
// 0 or less meant the default, and gives "". ok is false for anything
// else.
func oldSize(t string) (withUnit string, ok bool) {
	if n, err := strconv.ParseInt(t, 0, 64); err == nil {
		if n <= 0 {
			return "", true
		}
		return strconv.FormatInt(n, 10) + "G", true
	}
	if !api.IsBareNumber(t) {
		return "", false
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil && f <= 0 {
		return "", true
	}
	return t + "G", true
}

// sizeValue is the value of a size flag. old names the row of an old form
// that still reads a bare number as GiB, and warns.
type sizeValue struct {
	bytes int64
	text  string
	name  string // the flag's name, for the warning
	def   string // the default, as the user would write it
	old   string
	// oldText and newText are the first bare number that the flag took,
	// and its form with a unit. They stay when a later use replaces the
	// value, so that the old form still warns.
	oldText, newText string
}

// addSize adds the size flag name to fs, with the value that v holds as
// its default.
func addSize(fs *pflag.FlagSet, v *sizeValue, name, usage string) {
	v.name, v.def = name, v.text
	fs.Var(v, name, usage)
}

// Set reads a size and remembers the first old form that needs a warning.
func (v *sizeValue) Set(s string) error {
	n, bare, err := parseSize(s, liveOld(v.old))
	if err != nil {
		return err
	}
	v.bytes, v.text = n, strings.TrimSpace(s)
	if bare && v.oldText == "" {
		v.oldText, v.newText = v.text, v.def
		if withUnit, _ := oldSize(v.text); withUnit != "" {
			v.newText = withUnit
		}
	}
	return nil
}

// Type is the value name shown in help.
func (*sizeValue) Type() string { return "size" }

// String returns the last size as the user wrote it.
func (v *sizeValue) String() string { return v.text }

func (v *sizeValue) oldUse() (id, old, now string) {
	if v.oldText == "" {
		return "", "", ""
	}
	return v.old, "--" + v.name + " " + v.oldText, "--" + v.name + " " + v.newText
}

// parseDuration reads a time (cli-spec 10.3): a Go duration, such as 30s,
// 10m or 2h, and not below 0. With never, it also takes "never", which
// turns the time off: then off is true.
func parseDuration(s string, never bool) (d time.Duration, off bool, err error) {
	t := strings.TrimSpace(s)
	if never && strings.EqualFold(t, "never") {
		return 0, true, nil
	}
	d, err = time.ParseDuration(t)
	if err != nil || d < 0 {
		if never {
			return 0, false, fmt.Errorf("%q is not a time, such as 30s, 10m or 2h, or never", s)
		}
		return 0, false, fmt.Errorf("%q is not a time, such as 30s, 10m or 2h", s)
	}
	return d, false, nil
}

// parseIdle reads an idle time, by the one rule that every idle time
// shares (host idle, host create --idle-stop, and host set idle= of task
// C7): the value is trimmed, "never" in any case turns the stop off and is
// kept as "never", and any other value is a Go duration of a minute or
// more. It returns the value to keep.
func parseIdle(s string) (string, error) {
	d, off, err := parseDuration(s, true)
	switch {
	case err != nil:
		return "", err
	case off:
		return "never", nil
	case d < time.Minute:
		return "", fmt.Errorf("%q is less than a minute", strings.TrimSpace(s))
	}
	return strings.TrimSpace(s), nil
}

// neverValue is the value of an idle time flag, read by parseIdle. An
// empty value is the default, as it was before the flag read times.
type neverValue struct{ text *string }

// addNever adds the time flag name to fs.
func addNever(fs *pflag.FlagSet, p *string, name, usage string) {
	fs.Var(&neverValue{text: p}, name, usage)
}

// Set reads an idle time, with an empty value keeping the default.
func (v *neverValue) Set(s string) error {
	if strings.TrimSpace(s) == "" {
		*v.text = ""
		return nil
	}
	idle, err := parseIdle(s)
	if err != nil {
		return err
	}
	*v.text = idle
	return nil
}

// Type is the value name shown in help.
func (*neverValue) Type() string { return "duration" }

// String returns the idle time to keep.
func (v *neverValue) String() string { return *v.text }
