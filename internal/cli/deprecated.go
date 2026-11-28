package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Old names and their warnings (docs/contributing/cli-spec.md 3). An old
// form keeps working for a time, and warns once per run. Its end is two
// releases after the version that adds the warning, and 12 months after
// its date, whichever comes later. After the end, a release removes the
// row, and the old name moves to the synonyms of errors.go.

// oldForm is one row of the table of old forms.
type oldForm struct {
	// Old and New are the old form and the new one, as the user types them.
	Old, New string
	// Kind is what changed: "command", "flag", "short flag", "behaviour",
	// "value", "JSON key" or "keymap name".
	Kind string
	// Cmd is the command whose reference page lists the old form, such as
	// "host ls".
	Cmd string
	// Version is the release that adds the warning, such as "0.2.0". Its
	// day is in releaseDays.
	Version string
	// Code names the warning in machine output.
	Code string
	// Says is the warning's first sentence. It is `"<old>" is now "<new>".`
	// when empty.
	Says string
	// Keeps says what works until the end. It is `"<old>" works` when empty.
	Keeps string
	// From, for a behaviour that changes at the end, says what holds from
	// then, without its full stop, such as `without a terminal, "rm" needs
	// -y`. The warning is then that sentence with the end.
	From string
}

// releaseDays holds the day of each release that adds a warning, as
// 2006-01-02. The 12 months of an old form start on the day of its release,
// when users first see the warning. The release step of
// docs/contributing/releasing.md sets the day of a new release here.
var releaseDays = map[string]string{
	"0.2.0": "2026-11-11",
}

// oldForms is every old form that still works. Each task of cli-spec 3.2
// adds its rows.
var oldForms = []oldForm{
	{
		Old: `the Go keys of "host ls --json"`, New: "snake_case keys", Kind: "JSON key", Cmd: "host ls",
		Version: "0.2.0", Code: "old_keys",
		Says:  `"host ls --json" now prints its keys in snake_case too, such as "ssh_config" for "SSHConfig".`,
		Keeps: "The old keys print",
	},
	{Old: "kill", New: "rm", Kind: "command", Cmd: "rm", Version: "0.2.0", Code: "old_name"},
	{Old: "flip", New: "attach", Kind: "command", Cmd: "attach", Version: "0.2.0", Code: "old_name"},
	{
		Old: `"rm" without -y, with input off`, New: "rm -y", Kind: "behaviour", Cmd: "rm",
		Version: "0.2.0", Code: "rm_needs_yes", From: `without a terminal, "rm" needs -y`,
	},
	{
		Old: "host idle", New: "host set <host> idle=<duration>|never", Kind: "command", Cmd: "host set",
		Version: "0.2.0", Code: "old_command",
	},
	{
		Old: "host destroy", New: "host rm <host> --destroy", Kind: "command", Cmd: "host rm",
		Version: "0.2.0", Code: "old_command",
	},
	{
		Old: "--force", New: "--reinstall", Kind: "flag", Cmd: "agents install",
		Version: "0.2.0", Code: "old_flag",
	},
}

// The rows that the commands warn with. The release that removes the row
// of rm without -y, at its end, sets oldRmWithoutYes to nil: rm with input
// off and without -y then fails with exit code 4 (question C-10).
var (
	// oldHostKeys is the row of the Go keys of `host ls --json`.
	oldHostKeys = oldRow(`the Go keys of "host ls --json"`)
	// oldKill and oldFlip are the old names of rm -y and attach.
	oldKill = oldRow("kill")
	oldFlip = oldRow("flip")
	// oldRmWithoutYes is rm that deletes with input off and without -y.
	oldRmWithoutYes = oldRow(`"rm" without -y, with input off`)
	// oldHostIdle and oldHostDestroy are the old names of host set idle=... and host rm --destroy.
	oldHostIdle    = oldRow("host idle")
	oldHostDestroy = oldRow("host destroy")
	// oldInstallForce is the row of `agents install --force` (cli-spec 12.2).
	oldInstallForce = oldRow("--force")
)

// oldRow is the row of the old form old.
func oldRow(old string) *oldForm {
	for i := range oldForms {
		if oldForms[i].Old == old {
			return &oldForms[i]
		}
	}
	panic("deprecated.go: no row for " + old)
}

// end is the release and the day of the old form's end: two releases, and
// 12 months, after the release that adds its warning.
func (o oldForm) end() (string, time.Time) {
	day, err := time.Parse(time.DateOnly, releaseDays[o.Version])
	if err != nil {
		panic("deprecated.go: release " + o.Version + " of " + o.Old + " has no day in releaseDays")
	}
	return twoReleasesAfter(o.Version), day.AddDate(1, 0, 0)
}

// oldNamesOf lists the old names of the command c that still work, such
// as "kill" for rm. A suggestion counts them as names of c, so a word near
// an old name suggests the new one: help leaves the old names out.
func oldNamesOf(c *cobra.Command) []string {
	var out []string
	for _, o := range oldForms {
		if o.Kind == "command" && o.New == shortPath(c) {
			out = append(out, o.Old)
		}
	}
	return out
}

// OldName is an old form that still works, for the reference page of its
// command.
type OldName struct {
	Old, New, Until string
}

// OldNames lists the old forms of the command at path, such as "host ls",
// with their end (cli-spec 3.1, rule 7).
func OldNames(path string) []OldName {
	var out []OldName
	for _, o := range oldForms {
		if o.Cmd == path {
			version, day := o.end()
			out = append(out, OldName{Old: o.Old, New: o.New, Until: fmt.Sprintf("v%s and %s, whichever comes later", version, day.Format("2 January 2006"))})
		}
	}
	return out
}

// twoReleasesAfter is the version two releases after v. A release is a new
// minor or major version, so 0.2.0 gives 0.4.0.
func twoReleasesAfter(v string) string {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) < 2 {
		panic("deprecated.go: " + v + " is not a version")
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		panic("deprecated.go: " + v + " is not a version")
	}
	return parts[0] + "." + strconv.Itoa(minor+2) + ".0"
}

// says is the warning's first sentence.
func (o oldForm) says() string {
	switch {
	case o.Says != "":
		return o.Says
	case o.From != "":
		return sentence(o.From)
	}
	return fmt.Sprintf("%q is now %q.", o.Old, o.New)
}

// warning is the whole warning, without "Warning:".
func (o oldForm) warning() string {
	version, day := o.end()
	if o.From != "" {
		return fmt.Sprintf("%s from v%s and %s, whichever comes later.", o.From, version, day.Format("2 January 2006"))
	}
	keeps := o.Keeps
	if keeps == "" {
		keeps = fmt.Sprintf("%q works", o.Old)
	}
	return fmt.Sprintf("%s %s until v%s and %s, whichever comes later.", o.says(), keeps, version, day.Format("2 January 2006"))
}

// warnOld prints the warning of an old form on stderr, once per run: one
// line, in yellow when stderr has colour, or one JSON line with --json. It
// prints nothing during shell completion.
func warnOld(cmd *cobra.Command, o *oldForm) {
	if cmd.Name() == cobra.ShellCompRequestCmd || cmd.Name() == cobra.ShellCompNoDescRequestCmd {
		return
	}
	root := cmd.Root()
	key := "warned:" + o.Old
	if root.Annotations[key] != "" {
		return
	}
	if root.Annotations == nil {
		root.Annotations = map[string]string{}
	}
	root.Annotations[key] = "yes"
	if jsonOn(cmd) {
		version, day := o.end()
		line, _ := json.Marshal(map[string]any{"v": jsonVersion, "warning": map[string]string{
			"code": o.Code, "message": o.says(), "old": o.Old, "new": o.New,
			"until_version": version, "until_date": day.Format(time.DateOnly),
		}})
		fmt.Fprintf(os.Stderr, "%s\n", line)
		return
	}
	yellow, reset := "\x1b[33m", "\x1b[0m"
	if !colourOn(os.Stderr) {
		yellow, reset = "", ""
	}
	fmt.Fprintf(os.Stderr, "%sWarning: %s%s\n", yellow, o.warning(), reset)
}
