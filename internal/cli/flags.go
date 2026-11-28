package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// Shared flags and value parsers (docs/contributing/cli-spec.md 10.2 and 10.3).
// Every flag in section 10.2 is defined through a helper here, which marks it
// so that a test catches any command that defines one of them by hand.

const (
	// sharedFlagAnnotation marks a flag registered by one of the shared helpers
	// of cli-spec 10.2.
	sharedFlagAnnotation = "cli-shared-flag"
	// oldFlagAnnotation marks a hidden flag that keeps an old form of cli-spec 3.
	oldFlagAnnotation = "cli-old-flag"
	// oldShortFlagAnnotation marks a hidden old flag whose user-facing name is
	// only its short letter (such as -d, -n or -v).
	oldShortFlagAnnotation = "cli-old-short"
)

func markFlag(f *pflag.Flag, key, val string) {
	if f == nil {
		return
	}
	if f.Annotations == nil {
		f.Annotations = map[string][]string{}
	}
	f.Annotations[key] = []string{val}
}

func markSharedFlag(f *pflag.Flag) {
	markFlag(f, sharedFlagAnnotation, "true")
}

func isSharedFlag(f *pflag.Flag) bool {
	return f != nil && len(f.Annotations[sharedFlagAnnotation]) > 0
}

func isOldFlag(f *pflag.Flag) bool {
	return f != nil && len(f.Annotations[oldFlagAnnotation]) > 0
}

func isOldShortFlag(f *pflag.Flag) bool {
	return f != nil && len(f.Annotations[oldShortFlagAnnotation]) > 0
}

func addYes(cmd *cobra.Command, p *bool, usage string) {
	cmd.Flags().BoolVarP(p, "yes", "y", false, usage)
	markSharedFlag(cmd.Flags().Lookup("yes"))
}

func addForce(cmd *cobra.Command, p *bool, usage string) {
	cmd.Flags().BoolVar(p, "force", false, usage)
	markSharedFlag(cmd.Flags().Lookup("force"))
}

func addForceShort(cmd *cobra.Command, p *bool, usage string) {
	cmd.Flags().BoolVarP(p, "force", "f", false, usage)
	markSharedFlag(cmd.Flags().Lookup("force"))
}

func addConfirm(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "confirm", "", usage)
	markSharedFlag(cmd.Flags().Lookup("confirm"))
}

func addName(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVarP(p, "name", "n", "", usage)
	markSharedFlag(cmd.Flags().Lookup("name"))
}

func addHostFlag(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "host", "", usage)
	markSharedFlag(cmd.Flags().Lookup("host"))
}

// addDetach defines --detach, and adds its -d short form once the old short
// form of new -d has ended (cli-spec 10.4).
func addDetach(cmd *cobra.Command, p *bool, usage string) {
	if oldNewShortDir == nil {
		cmd.Flags().BoolVarP(p, "detach", "d", false, usage)
	} else {
		cmd.Flags().BoolVar(p, "detach", false, usage)
	}
	markSharedFlag(cmd.Flags().Lookup("detach"))
}

func addMemoryMax(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "memory-max", "", usage)
	markSharedFlag(cmd.Flags().Lookup("memory-max"))
}

func addMemory(cmd *cobra.Command, p *string, def, usage string) {
	cmd.Flags().StringVar(p, "memory", def, usage)
	markSharedFlag(cmd.Flags().Lookup("memory"))
}

func addDisk(cmd *cobra.Command, p *string, def, usage string) {
	cmd.Flags().StringVar(p, "disk", def, usage)
}

func addCPUs(cmd *cobra.Command, p *int, def int, usage string) {
	cmd.Flags().IntVar(p, "cpus", def, usage)
	markSharedFlag(cmd.Flags().Lookup("cpus"))
}

func addSecret(cmd *cobra.Command, p *[]string, usage string) {
	cmd.Flags().StringSliceVar(p, "secret", nil, usage)
	markSharedFlag(cmd.Flags().Lookup("secret"))
}

func addFor(cmd *cobra.Command, p *time.Duration, def time.Duration, usage string) {
	cmd.Flags().DurationVar(p, "for", def, usage)
	markSharedFlag(cmd.Flags().Lookup("for"))
}

func addAll(cmd *cobra.Command, p *bool, usage string) {
	cmd.Flags().BoolVar(p, "all", false, usage)
	markSharedFlag(cmd.Flags().Lookup("all"))
}

func addFix(cmd *cobra.Command, p *bool, usage string) {
	cmd.Flags().BoolVar(p, "fix", false, usage)
	markSharedFlag(cmd.Flags().Lookup("fix"))
}

func addLabel(cmd *cobra.Command, p *[]string, usage string) {
	cmd.Flags().StringSliceVar(p, "label", nil, usage)
	markSharedFlag(cmd.Flags().Lookup("label"))
}

func addTokenCommand(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "token-command", "", usage)
	markSharedFlag(cmd.Flags().Lookup("token-command"))
}

func addImages(cmd *cobra.Command, p *string, usage string) {
	cmd.Flags().StringVar(p, "images", "", usage)
	markSharedFlag(cmd.Flags().Lookup("images"))
}

// oldFlagValue writes to the target flag's value so last-flag-wins semantics
// hold when both the old and new flags appear, and records that the old flag
// was used so the command warns once.
type oldFlagValue struct {
	target pflag.Value
	used   *bool
}

func (v *oldFlagValue) String() string { return v.target.String() }
func (v *oldFlagValue) Type() string   { return v.target.Type() }

func (v *oldFlagValue) Set(s string) error {
	if err := v.target.Set(s); err != nil {
		return err //nolint:wrapcheck // pflag wraps Set errors in InvalidValueError
	}
	*v.used = true
	return nil
}

// addOldFlag registers a hidden old long flag that writes to the flag named
// targetName on cmd and sets *used when passed.
func addOldFlag(cmd *cobra.Command, targetName, oldName string, used *bool, old *oldForm) {
	target := cmd.Flags().Lookup(targetName)
	if target == nil {
		panic("flags.go: missing target flag " + targetName)
	}
	cmd.Flags().Var(&oldFlagValue{target: target.Value, used: used}, oldName, "")
	f := cmd.Flags().Lookup(oldName)
	f.Hidden = true
	f.NoOptDefVal = target.NoOptDefVal
	markFlag(f, oldFlagAnnotation, old.Old)
}

// addOldShortFlag registers a hidden old short flag that writes to the flag
// named targetName on cmd and sets *used when passed.
func addOldShortFlag(cmd *cobra.Command, targetName, hiddenName, short string, used *bool, old *oldForm) {
	target := cmd.Flags().Lookup(targetName)
	if target == nil {
		panic("flags.go: missing target flag " + targetName)
	}
	cmd.Flags().VarP(&oldFlagValue{target: target.Value, used: used}, hiddenName, short, "")
	f := cmd.Flags().Lookup(hiddenName)
	f.Hidden = true
	f.NoOptDefVal = target.NoOptDefVal
	markFlag(f, oldFlagAnnotation, old.Old)
	markFlag(f, oldShortFlagAnnotation, short)
}

// addOldShortBoolFlag registers a standalone hidden old short boolean flag.
func addOldShortBoolFlag(cmd *cobra.Command, hiddenName, short string, p *bool, old *oldForm) {
	cmd.Flags().BoolVarP(p, hiddenName, short, false, "")
	f := cmd.Flags().Lookup(hiddenName)
	f.Hidden = true
	markFlag(f, oldFlagAnnotation, old.Old)
	markFlag(f, oldShortFlagAnnotation, short)
}

// parseNameList reads a list of names passed via repeated flags or commas
// (cli-spec 10.3). Empty entries are dropped. When allowEmpty is false, an
// empty result (such as "" or " , ") fails with exit code 2.
func parseNameList(flagName string, values []string, allowEmpty bool, emptyHint string) ([]string, error) {
	var out []string
	for _, raw := range values {
		for _, item := range strings.Split(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	}
	if len(out) == 0 && !allowEmpty {
		if emptyHint == "" {
			emptyHint = "Pass at least one value with " + flagName + "."
		}
		return nil, &client.UserError{
			Msg:  flagName + " needs at least one value",
			Hint: emptyHint,
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	return out, nil
}

// parseSizeFlag reads a size with a unit in powers of 1024 using api.ParseSize
// (cli-spec 10.3).
func parseSizeFlag(flagName, raw string) (int64, error) {
	n, err := api.ParseSize(raw)
	if err != nil {
		return 0, &client.UserError{
			Msg:  flagName + ": " + err.Error(),
			Hint: "Pass a size with a unit, such as " + api.MemorySizes + ".",
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	return n, nil
}

// parseLegacyGiBSizeFlag reads a size flag that also accepts a bare whole
// number as GiB until its old form ends (cli-spec 10.3). A bare number keeps
// the whole-number forms of Go's base-0 integer parser (010 for 8, 0x10 for
// 16), warns once, and treats 0 or less as the default (0).
func parseLegacyGiBSizeFlag(cmd *cobra.Command, flagName, raw string, old *oldForm) (int64, error) {
	s := strings.TrimSpace(raw)
	if old != nil {
		if n, err := strconv.ParseInt(s, 0, 64); err == nil {
			warnOld(cmd, old)
			if n <= 0 {
				return 0, nil
			}
			if n >= 1<<32 {
				return 0, &client.UserError{
					Msg:  fmt.Sprintf("%s: %q is too big", flagName, raw),
					Hint: "Pass a smaller size with a unit, such as " + api.MemorySizes + ".",
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			return n << 30, nil
		} else if errors.Is(err, strconv.ErrRange) {
			return 0, &client.UserError{
				Msg:  fmt.Sprintf("%s: %q is too big", flagName, raw),
				Hint: "Pass a smaller size with a unit, such as " + api.MemorySizes + ".",
				Code: "bad_value",
				Kind: client.KindUsage,
			}
		}
	}
	return parseSizeFlag(flagName, raw)
}

// parseDurationFlag reads a Go duration flag, optionally accepting "never" to
// turn the timer off. An empty string keeps the default (0, false, nil)
// (cli-spec 10.3).
func parseDurationFlag(flagName, raw string, allowNever bool, minDur time.Duration) (time.Duration, bool, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false, nil
	}
	if allowNever && s == "never" {
		return 0, true, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < minDur {
		hint := "Pass a duration such as 30m or 2h."
		if allowNever {
			hint = "Pass a duration such as 30m or 2h, or never."
		}
		return 0, false, &client.UserError{
			Msg:  fmt.Sprintf("%s: %q is not a valid duration", flagName, raw),
			Hint: hint,
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	return d, false, nil
}

// formatGiBText formats bytes in GiB for human progress lines.
func formatGiBText(bytes int64) string {
	if bytes%(1<<30) == 0 {
		return fmt.Sprintf("%d GiB", bytes>>30)
	}
	return strconv.FormatFloat(float64(bytes)/float64(1<<30), 'f', -1, 32) + " GiB"
}
