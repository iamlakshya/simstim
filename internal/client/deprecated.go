package client

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// OldForm is one row of the table of deprecated commands, flags, behaviours,
// JSON keys and keymap names (docs/contributing/cli-spec.md 3). Both the CLI
// and the launcher read from it so every warning carries the same words and
// removal date.
type OldForm struct {
	// ID is the canonical lookup key of the old form.
	ID string
	// Note is extra reference text for the old form.
	Note string
	// Old and New are the old form and the new one, as the user types them.
	Old, New string
	// Kind is what changed: "command", "flag", "short flag", "behaviour",
	// "value", "JSON key" or "keymap name".
	Kind string
	// Cmd is the command whose reference page lists the old form, such as
	// "host ls".
	Cmd string
	// Version is the release that adds the warning, such as "0.2.0". Its
	// day is in ReleaseDays.
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

// ReleaseDays holds the day of each release that adds a warning, as
// 2006-01-02. The 12 months of an old form start on the day of its release,
// when users first see the warning.
var ReleaseDays = map[string]string{
	"0.2.0": "2026-11-11",
}

// OldKeymapPreview is the deprecated [keymap] entry for session: peek, shared
// by config keymap and the launcher's startup notice.
var OldKeymapPreview = &OldForm{
	ID:  `[keymap] "session: preview"`,
	Old: `[keymap] "session: preview"`, New: `"session: peek"`, Kind: "keymap name", Cmd: "config keymap",
	Version: "0.2.0", Code: "old_name",
	Says:  `"session: preview" in [keymap] is now "session: peek".`,
	Keeps: `"session: preview" works`,
}

// End is the release and the day of the old form's end: two releases, and
// 12 months, after the release that adds its warning.
func (o OldForm) End() (string, time.Time) {
	day, err := time.Parse(time.DateOnly, ReleaseDays[o.Version])
	if err != nil {
		panic("deprecated: release " + o.Version + " of " + o.Old + " has no day in ReleaseDays")
	}
	return TwoReleasesAfter(o.Version), day.AddDate(1, 0, 0)
}

// TwoReleasesAfter is the version two releases after v. A release is a new
// minor or major version, so 0.2.0 gives 0.4.0.
func TwoReleasesAfter(v string) string {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) < 2 {
		panic("deprecated: " + v + " is not a version")
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		panic("deprecated: " + v + " is not a version")
	}
	return parts[0] + "." + strconv.Itoa(minor+2) + ".0"
}

// FirstSentence is the warning's opening sentence.
func (o OldForm) FirstSentence() string {
	switch {
	case o.Says != "":
		return o.Says
	case o.From != "":
		return strings.ToUpper(o.From[:1]) + o.From[1:] + "."
	}
	return fmt.Sprintf("%q is now %q.", o.Old, o.New)
}

// Warning is the whole warning, without "Warning:", including both the
// removal version and the removal date.
func (o OldForm) Warning() string {
	version, day := o.End()
	if o.From != "" {
		return fmt.Sprintf("%s from v%s and %s, whichever comes later.", o.From, version, day.Format("2 January 2006"))
	}
	keeps := o.Keeps
	if keeps == "" {
		keeps = fmt.Sprintf("%q works", o.Old)
	}
	return fmt.Sprintf("%s %s until v%s and %s, whichever comes later.", o.FirstSentence(), keeps, version, day.Format("2 January 2006"))
}
