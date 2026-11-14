package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Old names and their warnings (docs/contributing/cli-spec.md 3). An old
// form keeps working for a time, and warns once per run. Its end is two
// releases after the version that adds the warning, and 12 months after
// its date, whichever comes later. After the end, a release removes the
// row, and the old name moves to the synonyms of errors.go: a command to
// synonyms, a flag to flagSynonyms. A hidden flag that keeps an old flag is
// added only while its row is here, so removing the row removes the old
// form.

// oldForm is one row of the table of old forms.
type oldForm struct {
	// ID names a row that the code looks up, such as "new -d", as the table
	// of cli-spec 3.2 writes the old form.
	ID string
	// Old and New are the old form and the new one, as the user types them.
	Old, New string
	// Kind is what changed: "command", "flag", "short flag", "behaviour",
	// "value", "JSON key" or "keymap name".
	Kind string
	// Cmd is the command whose reference page lists the old form, such as
	// "host ls". It is "" for the root.
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
	// Note says more about the old form, for the reference.
	Note string
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
// adds its rows. Tests change it to see what a release does after an end.
var oldForms = []oldForm{
	{
		ID:  "host ls --json",
		Old: `the Go keys of "host ls --json"`, New: "snake_case keys", Kind: "JSON key", Cmd: "host ls",
		Version: "0.2.0", Code: "old_keys",
		Says:  `"host ls --json" now prints its keys in snake_case too, such as "ssh_config" for "SSHConfig".`,
		Keeps: "The old keys print",
	},
	{ID: "-v", Old: "-v", New: "--version", Kind: "short flag", Version: "0.2.0", Code: "old_name",
		Note: "No command has `-v`."},
	{ID: "new -d", Cmd: "new", Old: "-d", New: "-C", Kind: "short flag", Version: "0.2.0", Code: "old_name",
		Note: "After its end, `-d` is the short form of `--detach`."},
	{ID: "new --setup", Cmd: "new", Old: "--setup", New: "--setup-script", Kind: "flag", Version: "0.2.0", Code: "old_name",
		Note: `"Setup" then names only the guided setup.`},
	{ID: "new --no-setup", Cmd: "new", Old: "--no-setup", New: "--no-setup-script", Kind: "flag", Version: "0.2.0", Code: "old_name"},
	{ID: "agents install --force", Cmd: "agents install", Old: "--force", New: "--reinstall", Kind: "flag", Version: "0.2.0", Code: "old_name",
		Note: "--force goes on only where work is lost."},
	{ID: "host alerts set --token-cmd", Cmd: "host alerts set", Old: "--token-cmd", New: "--token-command", Kind: "flag", Version: "0.2.0", Code: "old_name"},
	{ID: "host alerts log -n", Cmd: "host alerts log", Old: "-n", New: "--lines", Kind: "short flag", Version: "0.2.0", Code: "old_name",
		Note: "`-n` then means only `--name`."},
	{ID: "host create --memory", Cmd: "host create", Old: "--memory 8", New: "--memory 8G", Kind: "value", Version: "0.2.0", Code: "old_name",
		Note: "A bare number means GiB. 0 or less means the default."},
	{ID: "host create --disk", Cmd: "host create", Old: "--disk 40", New: "--disk 40G", Kind: "value", Version: "0.2.0", Code: "old_name",
		Note: "A bare number means GiB. 0 or less means the default."},
	{ID: "kill", Old: "kill", New: "rm", Kind: "command", Cmd: "rm", Version: "0.2.0", Code: "old_name"},
	{ID: "flip", Old: "flip", New: "attach", Kind: "command", Cmd: "attach", Version: "0.2.0", Code: "old_name"},
	{
		ID: "rm without -y", Old: `"rm" without -y, with input off`, New: "rm -y", Kind: "behaviour", Cmd: "rm",
		Version: "0.2.0", Code: "rm_needs_yes", From: `without a terminal, "rm" needs -y`,
	},
	{
		ID: "host idle", Old: "host idle <host> <time>", New: "host set <host> idle=<time>", Kind: "command", Cmd: "host set",
		Version: "0.2.0", Code: "old_name",
		Keeps: `"host idle" works`,
	},
	{
		ID: "host destroy", Old: "host destroy <host> [-y] [--force]", New: "host rm <host> --destroy [--confirm <host>] [--force]", Kind: "command", Cmd: "host rm",
		Version: "0.2.0", Code: "old_name",
		Says:  `"host destroy <host>" is now "host rm <host> --destroy", and its -y is now --confirm <host>.`,
		Keeps: `"host destroy" works`,
	},
}

// The rows that the commands warn with. The release that removes the row
// of rm without -y, at its end, sets oldRmWithoutYes to nil: rm with input
// off and without -y then fails with exit code 4 (question C-10).
var (
	// oldHostIdle and oldHostDestroy are the old forms of host set and rm.
	oldHostIdle    = oldRow("host idle <host> <time>")
	oldHostDestroy = oldRow("host destroy <host> [-y] [--force]")
	// oldHostKeys is the row of the Go keys of `host ls --json`.
	oldHostKeys = oldRow(`the Go keys of "host ls --json"`)
	// oldKill and oldFlip are the old names of rm -y and attach.
	oldKill = oldRow("kill")
	oldFlip = oldRow("flip")
	// oldRmWithoutYes is rm that deletes with input off and without -y.
	oldRmWithoutYes = oldRow(`"rm" without -y, with input off`)
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
	Old, New, Until, Note string
}

// OldNames lists the old forms of the command at path, such as "host ls",
// with their end (cli-spec 3.1, rule 7). The root is "".
func OldNames(path string) []OldName {
	var out []OldName
	for _, o := range oldForms {
		if o.Cmd == path {
			version, day := o.end()
			out = append(out, OldName{Old: o.Old, New: o.New, Until: fmt.Sprintf("v%s and %s, whichever comes later", version, day.Format("2 January 2006")), Note: o.Note})
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

// warnOld prints the warning of an old form on the stderr of cmd, once per
// run: one line, in yellow when stderr has colour, or one JSON line with
// --json. It prints nothing during shell completion.
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
	w := cmd.ErrOrStderr()
	line := commandLine(cmd)
	if jsonAsked(cmd, line) {
		version, day := o.end()
		line, _ := json.Marshal(map[string]any{"v": jsonVersion, "warning": map[string]string{
			"code": o.Code, "message": o.says(), "old": o.Old, "new": o.New,
			"until_version": version, "until_date": day.Format(time.DateOnly),
		}})
		fmt.Fprintf(w, "%s\n", jsonSafe(line))
		return
	}
	fmt.Fprintln(w, oldWarning(o, colourOf(w) && !flagOn(cmd, line, "no-color")))
}

// oldWarning is the one line of the warning, in yellow with colour.
func oldWarning(o *oldForm, colour bool) string {
	line := "Warning: " + o.warning()
	if colour {
		return "\x1b[33m" + line + "\x1b[0m"
	}
	return line
}

// colourOf reports whether w should get colour: a terminal, with colour
// on.
func colourOf(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && colourOn(f)
}

// findOld returns the row of the old form id, while the table holds it.
func findOld(id string) (*oldForm, bool) {
	for i := range oldForms {
		if oldForms[i].ID == id {
			return &oldForms[i], true
		}
	}
	return nil, false
}

// liveOld reports whether the old form id still works: the table holds its
// row.
func liveOld(id string) bool {
	_, ok := findOld(id)
	return id != "" && ok
}

// oldKey marks a hidden flag that keeps an old form. Its value is the ID
// of the form's row.
const oldKey = "old_form"

// hideOld hides the flag name of fs, which keeps the old form id: help and
// completion leave it out, and a run that uses it warns.
func hideOld(fs *pflag.FlagSet, name, id string) {
	fs.Lookup(name).Hidden = true
	_ = fs.SetAnnotation(name, oldKey, []string{id})
}

// oldValue is the value of a flag that may take an old form, as a size
// that is a bare number does.
type oldValue interface {
	// oldUse returns the ID of the old form that the value took, and the
	// old and new forms with the user's own value, or "" for none.
	oldUse() (id, old, new string)
}

// warnOldFlags warns once about each old form that the flags of cmd took.
// It runs before the command, and again when the run ends, for a run that
// ended before its command ran, as -v does. During shell completion it
// warns about nothing: Cobra runs only its __complete command, which reads
// no flags.
func warnOldFlags(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		if ids := f.Annotations[oldKey]; len(ids) > 0 {
			if o, ok := findOld(ids[0]); ok {
				warnOld(cmd, o)
			}
		}
		if v, ok := f.Value.(oldValue); ok {
			if id, old, now := v.oldUse(); id != "" {
				if o, ok := findOld(id); ok {
					typed := *o
					typed.Old, typed.New = old, now
					warnOld(cmd, &typed)
				}
			}
		}
	})
}
