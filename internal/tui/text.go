package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// shown is text from a host or config as the launcher shows it on one line:
// without control or hidden characters.
func shown(s string) string {
	return api.CleanText(strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ").Replace(s), 4000)
}

func shownAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = shown(s)
	}
	return out
}

func cleanFolders(res client.FoldersResult) client.FoldersResult {
	return client.OfferFolders(res)
}

// cells is the width of s in terminal cells, ignoring escape sequences.
func cells(s string) int { return ansi.StringWidth(s) }

// trunc shortens s to at most w cells, ending with "…" when it cuts.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if cells(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// fit makes s exactly w cells wide: cut with "…" or padded with spaces.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = trunc(s, w)
	if n := cells(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// fitRight is fit, aligned to the right.
func fitRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = trunc(s, w)
	if n := cells(s); n < w {
		s = strings.Repeat(" ", w-n) + s
	}
	return s
}

// truncPath shortens a path in the middle, keeping its start and its end:
// "~/src/…/auth".
func truncPath(p string, w int) string {
	if w <= 0 {
		return ""
	}
	if cells(p) <= w {
		return p
	}
	// Keep as many whole parts as fit, the end first: "private/tmp/…/app/src".
	parts := strings.Split(p, "/")
	best, most := "", 0
	first := 1
	if parts[0] == "" {
		first = 2 // an absolute path keeps its first folder
	}
	for head := first; head < len(parts)-1; head++ {
		for tail := 1; head+tail < len(parts); tail++ {
			s := strings.Join(parts[:head], "/") + "/…/" + strings.Join(parts[len(parts)-tail:], "/")
			if n := head + tail*2; cells(s) <= w && n > most {
				best, most = s, n
			}
		}
	}
	if best != "" {
		return best
	}
	r := []rune(p)
	if w < 3 {
		return string(r[len(r)-w:])
	}
	head := (w - 1) / 2
	tail := w - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// seg is a piece of a row with its style.
type seg struct {
	s  string
	st lipgloss.Style
}

// plural picks the word for n.
func plural(n int, one, many string) string { return client.Plural(n, one, many) }

// fmtAge is a short age: 14s, 2m, 1h, 3d.
func fmtAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// fmtLatency is a short round-trip time: 9ms, 1.2s.
func fmtLatency(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Second:
		ms := d.Milliseconds()
		if ms < 1 {
			ms = 1
		}
		return fmt.Sprintf("%dms", ms)
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

// fmtSize is a size in binary units: 310 MiB, 6.1 GiB. The host details
// share it with `host show`.
func fmtSize(b int64) string { return client.SizeText(b) }

// fmtMemory is the memory line of a session: "6.1 of 8 GiB".
func fmtMemory(cur, limit int64) string {
	switch {
	case cur <= 0 && limit <= 0:
		return ""
	case limit <= 0:
		return fmtSize(cur)
	case cur <= 0:
		return "limit " + fmtSize(limit)
	}
	c, l := fmtSize(cur), fmtSize(limit)
	if strings.HasSuffix(c, " GiB") && strings.HasSuffix(l, " GiB") {
		return strings.TrimSuffix(c, " GiB") + " of " + l
	}
	return c + " of " + l
}

// fuzzy reports whether every letter of q appears in s in order, and scores
// the match: runs of letters and word starts count more, as do short strings.
func fuzzy(q, s string) (int, bool) {
	q = strings.ToLower(strings.Join(strings.Fields(q), ""))
	if q == "" {
		return 0, true
	}
	qr, sr := []rune(q), []rune(strings.ToLower(s))
	score, qi, last := 0, 0, -2
	for i, r := range sr {
		if qi == len(qr) {
			break
		}
		if r != qr[qi] {
			continue
		}
		score++
		if last == i-1 {
			score += 3
		}
		if i == 0 || !unicode.IsLetter(sr[i-1]) && !unicode.IsDigit(sr[i-1]) {
			score += 2
		}
		last = i
		qi++
	}
	if qi < len(qr) {
		return 0, false
	}
	return score*8 - len(sr), true
}

// fuzzyPos marks the letters of s that the filter q matches, in the order
// fuzzy finds them. It is nil when q is empty or does not match.
func fuzzyPos(q, s string) []bool {
	q = strings.ToLower(strings.Join(strings.Fields(q), ""))
	if q == "" {
		return nil
	}
	qr, sr := []rune(q), []rune(strings.ToLower(s))
	hit := make([]bool, len(sr))
	qi := 0
	for i, r := range sr {
		if qi < len(qr) && r == qr[qi] {
			hit[i] = true
			qi++
		}
	}
	if qi < len(qr) {
		return nil
	}
	return hit
}

// errTextMax is the most runes of an error's message or hint that the
// launcher draws.
const errTextMax = api.BlockMax

// errText splits an error into its message and its "Try:" hint, each
// cleaned and cut short. An error can carry what a host wrote to stderr,
// such as a failed connection's, so no control or hidden character of it
// reaches the screen (isolation spec 14.15). Its lines stay lines.
func errText(err error) (msg, hint string) {
	if err == nil {
		return "", ""
	}
	// An error can hold what a host sent.
	if ue, ok := asUserError(err); ok {
		return cleanLines(ue.Msg), cleanLines(ue.Hint)
	}
	return cleanLines(err.Error()), ""
}

// cleanLines is s cleaned line by line, with each line break as one
// newline, and cut to errTextMax runes: an SSH error of three lines of
// stderr keeps its three lines, which wrap draws one by one.
func cleanLines(s string) string { return api.CleanBlock(s, errTextMax) }

// oneLine is s, such as the message of errText, with each line break as a
// space: for the places that draw an error on one line, such as a row of a
// list or a field. A break there would start a line outside the place.
func oneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }

// sentence makes a message start with a capital letter and end with a stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	s = string(r)
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, ")") {
		s += "."
	}
	return s
}

// wrap breaks s into lines of at most w cells, at spaces where it can.
func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, strings.Split(ansi.Wrap(para, w, ""), "\n")...)
	}
	return out
}

// joinWrap joins items with dots, in lines of at most w cells. It breaks
// only between items, so an item never splits. It returns one empty line
// for no items.
func joinWrap(items []string, w int) []string {
	lines := []string{""}
	for _, s := range items {
		n := len(lines) - 1
		switch {
		case lines[n] == "":
			lines[n] = s
		case cells(lines[n]+dot+s) <= w:
			lines[n] += dot + s
		default:
			lines = append(lines, s)
		}
	}
	return lines
}
