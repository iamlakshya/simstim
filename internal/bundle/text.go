package bundle

import (
	"bytes"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/version"
)

// readme is README.txt: what each file is, and the tool that reads it.
func readme(now time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "A debug bundle of %s %s, written on %s.\n\n", brand.DisplayName, version.Version, now.UTC().Format("2 January 2006 at 15:04 UTC"))
	b.WriteString("Read every file before you attach the bundle to an issue. The bundle replaced the\n" +
		"private values that it knows with placeholders, such as <host-1> (replaced.txt).\n" +
		"But a log line can hold a name that the bundle does not know. Nothing was uploaded.\n\n")
	b.WriteString("What is where:\n")
	rows := [][2]string{
		{"laptop/", "The laptop's debug folder, and doctor.txt, the output of `" + brand.Name + " doctor`"},
		{"laptop/outside/", "Debug files that --debug-file put outside the debug folder, by number"},
		{"host/doctor.json", "The output of `" + brand.Name + " host doctor`"},
		{"host/ssh.log", "With --ssh: the ssh -v log of one new connection"},
		{"host/bridge/", "The debug files of the host's bridges, one for each connection"},
		{"host/sessions/<id>/", "One session: debug.jsonl, holder.log, unit.txt and kernel.txt"},
		{"host/sessions/<id>/debug/", "The session's profiles and the saved windows of its flight recorder"},
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-28s %s\n", r[0], r[1])
	}
	b.WriteString("\nThe files, and the tools that read them:\n")
	rows = [][2]string{
		{"*.jsonl", "Records and debug lines, one JSON object a line. Read them as they are, or with test/keytrace"},
		{"*.log", "Logs, as text"},
		{"*.pprof", "Profiles: go tool pprof <file>"},
		{"*.trace", "Saved windows of the flight recorder: go tool trace <file>"},
		{"unit.txt", "The state of the session's systemd unit, and its memory.events"},
		{"kernel.txt", "The kernel's messages that name the session's unit, such as an out-of-memory kill"},
		{"*.1", "The older part of a file that turned over"},
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-12s %s\n", r[0], r[1])
	}
	return b.Bytes()
}

// replacedFile is replaced.txt, checked as every file is: it names files
// and gives reasons. When it still holds a private value, it keeps only
// the counts.
func replacedFile(r *Result, s *Scrubber) []byte {
	text := replacedText(r, s)
	if s.Check(text) == "" {
		return text
	}
	return append(replacedText(&Result{Counts: r.Counts}, s),
		"\nThe list of files left out held a private value, so the bundle left the list out.\n"...)
}

// replacedText is replaced.txt: each kind of value that the bundle
// replaced, and how many times, never the values. It also lists the files
// that it left out, and what the host could not gather, with the values
// replaced there too.
func replacedText(r *Result, s *Scrubber) []byte {
	var b bytes.Buffer
	b.WriteString("The bundle replaced these kinds of values with placeholders. One value has the same\n" +
		"placeholder in every file. The values themselves are not in the bundle.\n\n")
	if len(r.Counts) == 0 {
		b.WriteString("  nothing\n")
	}
	for _, c := range r.Counts {
		fmt.Fprintf(&b, "  %-28s %d\n", kindText[c.Kind].many, c.N)
	}
	if len(r.Left) > 0 {
		b.WriteString("\nFiles left out:\n")
		for _, l := range r.Left {
			fmt.Fprintf(&b, "  %s: %s\n", s.Scrub([]byte(l.Name)), s.Scrub([]byte(l.Why)))
		}
	}
	if len(r.Notes) > 0 {
		b.WriteString("\nWhat the host did not gather:\n")
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "  %s\n", s.Scrub([]byte(n)))
		}
	}
	return b.Bytes()
}
