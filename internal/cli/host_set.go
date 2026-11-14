package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/provider/e2b"
	"github.com/iamlakshya/simstim/internal/provider/lima"
)

// host set (docs/contributing/cli-spec.md 11.1) changes named settings of
// a host, and only those. host idle is its old form. The isolation plan
// adds its keys to hostSettings: label.<key>, tier, floor and drain.

// hostSetting is one key that host set changes.
type hostSetting struct {
	// key names it, as in idle=2h.
	key string
	// about says what it is, and which values it takes, for the help.
	about string
	// check reads the value for h, and returns it as it is kept, or a
	// usage error.
	check func(h client.Host, value string) (string, error)
	// now is the value on h, for the line that says what changed.
	now func(h client.Host) string
	// current reads a value kept outside h. Its error stops every change.
	current func(ctx context.Context, h client.Host) (string, error)
	// save changes the saved entry of h, for a key kept in hosts.toml. It
	// is nil for a key that lives on the host or in tiers.toml.
	save func(h *client.Host, value string)
	// where names where a setting without save took effect, such as "in
	// tiers.toml". An empty string defaults to "on <host>".
	where string
	// apply makes the new value take effect after the save, and may dial
	// the host. It says on w what it did. It is nil when the save is all.
	apply func(ctx context.Context, w io.Writer, h client.Host, value string) error
}

// hostSettings are the keys of host set. A key that this build does not
// have is unknown (cli-spec 11.1, rule 2).
var hostSettings = []hostSetting{
	{key: "idle", about: "how long a host that `host create` made may idle before it stops. It takes a\n" +
		"time of a minute or more, such as 30m or 2h, or never.", check: checkIdle, now: idleNow, save: func(h *client.Host, v string) { h.Machine.IdleStop = v }, apply: applyIdleSetting},
	{key: "tier", about: "who else has root on the host: own, throwaway or shared. Lowering works at once.\n" +
		"Raising connects to check the pinned host key, and asks for the host's typed name.", check: checkTier, current: tierNowContext, where: "in tiers.toml", apply: applyTierSetting},
}

func hostSetCmd() *cobra.Command {
	var confirm string
	long := make([]string, 0, 1+len(hostSettings))
	long = append(long, "Change named settings of a host, and only those. It checks every pair before it changes\n"+
		"anything, then applies them in the order given.")
	for _, s := range hostSettings {
		long = append(long, s.key+": "+s.about)
	}
	cmd := &cobra.Command{
		Use:   "set <host> <key>=<value>...",
		Short: "Change a host's settings, such as idle or tier",
		Long:  strings.Join(long, "\n\n"),
		Example: "  " + brand.Name + " host set lima idle=2h\n  " + brand.Name + " host set lima idle=never\n  " +
			brand.Name + " host set my-vps tier=shared\n  " + brand.Name + " host set my-vps tier=own --confirm my-vps",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setHostWith(cmd.Context(), prompts(cmd), os.Stderr, args[0], args[1:], confirm)
		},
	}
	addConfirm(cmd, &confirm, "the name of the `host`, typed here instead of at the question when raising its tier")
	return cmd
}

// hostIdleCmd is the old form of host set <host> idle=<time>.
func hostIdleCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "idle <host> <duration>|never",
		Short:   "Set a created host's idle time (old form of host set)",
		Hidden:  true,
		Example: "  " + brand.Name + " host set lima idle=30m",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnOld(cmd, oldHostIdle)
			return setHost(cmd.Context(), os.Stderr, args[0], []string{"idle=" + args[1]})
		},
	}
}

// setIdle installs, or with never removes, the helper's idle timer on a
// VM. Tests replace it.
var setIdle = onboard.SetIdle

// pair is one key=value of host set, as checked.
type pair struct {
	text  string // as the user typed it
	s     *hostSetting
	value string // as it is kept
}

type setCallKey struct{}

type setCall struct {
	p       *prompter
	confirm string
}

// setHost changes the settings of host name that pairs give. It checks
// every pair first, saves the keys kept in hosts.toml in one write, then
// applies each pair in order. Each change prints one line on w. When a
// pair fails, the error says which pairs took effect.
func setHost(ctx context.Context, w io.Writer, name string, words []string) error {
	return setHostWith(ctx, &prompter{ctx: ctx, out: w, path: brand.Name + " host set"}, w, name, words, "")
}

func setHostWith(ctx context.Context, pr *prompter, w io.Writer, name string, words []string, confirm string) error {
	h, err := client.FindHost(name)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return &client.UserError{Msg: "host set needs a setting, such as idle=2h", Hint: brand.Name + " host show " + shellWord(h.Name),
			Code: "missing_setting", Kind: client.KindUsage}
	}
	// The lock of the host, until the set ends. The entry is read again
	// under it, so the save changes only the keys given.
	beforeHostLock(h.Name)
	ctx, unlock, err := client.LockHost(ctx, h.Name, "host set")
	if err != nil {
		return err
	}
	defer unlock()
	if h, err = client.FindHost(h.Name); err != nil {
		return err
	}
	pairs, err := readPairs(h, words)
	if err != nil {
		return err
	}
	if err := checkSetConfirm(ctx, pr, h, pairs, confirm); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, setCallKey{}, setCall{p: pr, confirm: confirm})
	changed, before, saves, err := prepareSetPairs(ctx, h, pairs)
	if err != nil {
		return err
	}
	var took []string
	if saves {
		if err := client.UpdateHosts(ctx, client.HostChange{Kind: "host set", Was: &h, Now: &changed}); err != nil {
			return err
		}
		took = recordSavedPairs(pr, w, h, pairs, before)
	}
	for i, p := range pairs {
		if p.s.apply == nil {
			continue
		}
		if err := p.s.apply(ctx, w, changed, p.value); err != nil {
			if isCancel(err) || ctx.Err() != nil {
				return pr.cancelOf(err)
			}
			return setFailed(h, pairs[i:], err, took)
		}
		place := p.s.where
		if place == "" {
			place = "on " + h.Name
		}
		if p.s.save == nil {
			// A key kept outside hosts.toml changed once apply succeeded.
			fmt.Fprintf(w, "%s: %s %s → %s.\n", h.Name, p.s.key, before[i], p.value)
			pr.changes().Add("set %s %s for %s", p.text, place, h.Name)
		}
		took = append(took, p.text+" "+place)
	}
	return nil
}

// readPairs checks each word: a known key, given once, with a good value.
func readPairs(h client.Host, words []string) ([]pair, error) {
	var out []pair
	seen := map[string]bool{}
	for i, wd := range words {
		key, value, ok := strings.Cut(wd, "=")
		if !ok {
			return nil, &client.UserError{Msg: fmt.Sprintf("%q is not a setting, which has the form key=value, such as idle=2h", wd),
				Hint: brand.Name + " host set --help", Code: "bad_setting", Kind: client.KindUsage}
		}
		s := findSetting(key)
		if s == nil {
			if near := nearestSetting(key); near != "" && hasKey(words, i, near) {
				// The key it may mean is given already: the same as twice.
				return nil, settingTwice(h, words, i, near)
			}
			return nil, unknownSetting(h, words, i, key, value)
		}
		if seen[key] {
			return nil, settingTwice(h, words, i, key)
		}
		seen[key] = true
		v, err := s.check(h, value)
		if err != nil {
			return nil, err
		}
		out = append(out, pair{text: wd, s: s, value: v})
	}
	return out, nil
}

func findSetting(key string) *hostSetting {
	for i := range hostSettings {
		if hostSettings[i].key == key {
			return &hostSettings[i]
		}
	}
	return nil
}

// settingTwice is the error of a key given twice. Its Try: line is the
// command line without the word at index at, which gives it again.
func settingTwice(h client.Host, words []string, at int, key string) error {
	rest := append(append([]string{"host", "set", h.Name}, words[:at]...), words[at+1:]...)
	return &client.UserError{Msg: fmt.Sprintf("host set got %s twice", key), Hint: brand.Name + " " + joinWords(rest),
		Code: "setting_twice", Kind: client.KindUsage}
}

// hasKey reports whether a word of words other than the one at index at
// gives key.
func hasKey(words []string, at int, key string) bool {
	for i, w := range words {
		if k, _, _ := strings.Cut(w, "="); i != at && k == key {
			return true
		}
	}
	return false
}

// settingNames are the keys as suggest takes them.
func settingNames() []named {
	things := make([]named, len(hostSettings))
	for i, s := range hostSettings {
		things[i] = named{s.key, []string{s.key}}
	}
	return things
}

// nearestSetting is the key that key may mean, or "".
func nearestSetting(key string) string {
	if cands := suggest(key, settingNames(), false); len(cands) > 0 {
		return cands[0]
	}
	return ""
}

// unknownSetting is the error of a key that host set does not have, with
// the keys it may mean (cli-spec 4.3). The Try: line is the command line
// with the nearest key in place of the typed one.
func unknownSetting(h client.Host, words []string, at int, key, value string) error {
	cands := suggest(key, settingNames(), false)
	try := brand.Name + " host set --help"
	if len(cands) > 0 {
		fixed := append([]string{"host", "set", h.Name}, words...)
		fixed[3+at] = cands[0] + "=" + value
		try = brand.Name + " " + joinWords(fixed)
	}
	return &usageError{
		UserError:   &client.UserError{Msg: fmt.Sprintf("host set has no setting %q", key), Hint: try, Code: "unknown_setting", Kind: client.KindUsage},
		suggestions: cands,
	}
}

// setFailed is the error of the first of rest, whose change failed. It
// says which pairs took effect before it (cli-spec 11.1, rule 7), and the
// command that runs the pairs that are left again. Its Try: line is the
// fix of the error itself, when it has one, or else that command.
func setFailed(h client.Host, rest []pair, err error, took []string) error {
	done := "Nothing took effect"
	if len(took) > 0 {
		done = "These took effect: " + strings.Join(took, ", ")
	}
	texts := make([]string, len(rest))
	for i, p := range rest {
		texts[i] = p.text
	}
	again := brand.Name + " " + joinWords(append([]string{"host", "set", h.Name}, texts...))
	ue := &client.UserError{Code: "set_failed", Hint: again}
	msg := err.Error()
	var inner *client.UserError
	if errors.As(err, &inner) {
		msg, ue.Code, ue.Kind, ue.Data = inner.Msg, inner.Code, inner.Kind, inner.Data
		if inner.Hint != "" {
			ue.Hint = inner.Hint
			done += ". After the fix, run " + again
		}
	}
	ue.Msg = fmt.Sprintf("%s failed on %s: %s. %s", rest[0].text, h.Name, strings.TrimSuffix(msg, "."), done)
	return ue
}

// checkIdle reads an idle time: a time of a minute or more, or never.
// Only a host that `host create` made has one.
func checkIdle(h client.Host, v string) (string, error) {
	if h.Machine == nil {
		return "", &client.UserError{Msg: h.Name + " was not made by host create, so it has no idle time",
			Hint: "Use a host made by " + brand.Name + " host create.", Code: "not_created", Kind: client.KindUsage}
	}
	t, err := parseIdle(v)
	if err != nil {
		return "", &client.UserError{Msg: fmt.Sprintf("%q is not an idle time", v), Hint: "Use a duration of a minute or more, such as 30m or 2h, or never.",
			Code: "bad_value", Kind: client.KindUsage}
	}
	return t, nil
}

// idleNow is the idle time of h, or its provider's default when it has
// none of its own.
func idleNow(h client.Host) string {
	if h.Machine == nil {
		return "never"
	}
	if h.Machine.IdleStop != "" {
		return h.Machine.IdleStop
	}
	switch h.Machine.Provider {
	case "lima":
		return lima.DefaultIdleStop
	case "e2b":
		return strings.TrimSuffix(e2b.DefaultIdleLease.String(), "0s")
	}
	return "never"
}

// idleSaid writes what the helper said of the idle stop that it set. The
// host sends it, so it is cleaned and cut as the launcher's errors are
// (api.BlockMax), and its lines stay lines.
func idleSaid(w io.Writer, out string) {
	fmt.Fprintf(w, "  ✓ idle stop: %s\n", api.CleanBlock(out, api.BlockMax))
}

// applyIdleSetting makes a new idle time take effect: a VM gets the
// helper's idle timer, and a sandbox's lease reads it at the next attach.
func applyIdleSetting(ctx context.Context, w io.Writer, h client.Host, v string) error {
	switch {
	case powersOffWhenIdle(h.Machine.Provider):
		out, err := setIdle(ctx, h)
		if err != nil {
			return err
		}
		idleSaid(w, out)
	case h.Machine.Provider == "e2b" && v == "never":
		fmt.Fprintf(w, "%s pauses only when its busy lease runs out, after the last client goes.\n", h.Name)
	case h.Machine.Provider == "e2b":
		fmt.Fprintf(w, "%s pauses %s after the last client goes, while no session works.\n", h.Name, v)
	default:
		fmt.Fprintf(w, "Saved, but %s hosts do not stop by themselves.\n", h.Machine.Provider)
	}
	return nil
}
