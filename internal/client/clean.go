package client

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A host's answers are not trusted: a session can write its own reason, and
// a host can send any text. The client cleans the text that it draws from a
// host with api.CleanText, so that no control, bidirectional, zero-width or
// tag character reaches the user's terminal through the client's own
// screens. Cleaning changes some text, so it makes copies to draw. The
// values that go back to a host (IDs, names, folders, secret names) stay
// as the host sent them, and so does JSON output, which escapes them
// instead. The errors in a host's answers are only drawn, so they are
// cleaned as they arrive (hostError, tailStderr), and so is what a host
// writes to the user's terminal while the client sets it up (cleanWriter).
// A command that the client prints or copies names a session by its ID,
// never by a name that the host chose (CommandTarget).

// The most runes of one string from a host that the client draws.
const (
	textMax = 1024 // a name, a state, a reason or a message
	pathMax = 4096 // a path, as PATH_MAX on Linux
)

// CleanLine cleans one line of text from a host, to draw.
func CleanLine(s string) string { return api.CleanText(s, textMax) }

// CleanPath cleans a path on a host, to draw.
func CleanPath(s string) string { return api.CleanText(s, pathMax) }

// ShowPath is a path from a host as a question shows it before the user
// answers: cleaned, or, when cleaning would change it, quoted with every
// character that is not ASCII escaped, so that the user sees what it holds.
func ShowPath(p string) string {
	if c := CleanPath(p); c != p {
		return strconv.QuoteToASCII(p)
	}
	return p
}

// OSCText keeps text from a host safe inside an OSC string, such as a
// desktop notification: it cleans the text, and turns each ";", which
// ends a field, into a space.
func OSCText(s string) string { return oscText(s, textMax) }

// oscText is OSCText that keeps at most maxRunes runes.
func oscText(s string, maxRunes int) string {
	return strings.ReplaceAll(api.CleanText(s, maxRunes), ";", " ")
}

// CommandTarget is host/session as a command that the client prints,
// suggests or copies names it. It names the session by its ID, never by a
// name that the host chose, and quotes the target for a POSIX shell when it
// holds anything else than letters, digits and . _ / -. So pasting the
// command runs nothing but the command.
func CommandTarget(host, id string) string { return ShellQuote(CleanLine(host + "/" + id)) }

// ShellQuote quotes s as one word for a POSIX shell. A word of letters,
// digits and . _ / - stays as it is.
func ShellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cleanWriter passes what a host writes for the user's terminal on, such
// as ssh's stderr, cleaned (api.CleanText). It keeps each line break, and
// passes a line on as it comes, so that a prompt without a line break shows
// at once. It holds back only a character that a write cut in two, until
// Flush.
type cleanWriter struct {
	w    io.Writer
	tail []byte
}

func (c *cleanWriter) Write(p []byte) (int, error) {
	buf := slices.Concat(c.tail, p)
	c.tail = nil
	if n := cutRune(buf); n > 0 {
		c.tail = append([]byte(nil), buf[len(buf)-n:]...)
		buf = buf[:len(buf)-n]
	}
	var out []byte
	for len(buf) > 0 {
		line, rest, nl := bytes.Cut(buf, []byte("\n"))
		out = append(out, api.CleanText(string(line), len(line))...)
		if nl {
			out = append(out, '\n')
		}
		buf = rest
	}
	if len(out) > 0 {
		if _, err := c.w.Write(out); err != nil {
			return 0, err //nolint:wrapcheck // the terminal's own error, passed on as a writer does
		}
	}
	return len(p), nil
}

// Flush writes what Write held back, cleaned.
func (c *cleanWriter) Flush() {
	if len(c.tail) > 0 {
		_, _ = c.w.Write([]byte(api.CleanText(string(c.tail), len(c.tail))))
		c.tail = nil
	}
}

// cutRune is how many bytes at the end of b start a character that is not
// whole yet.
func cutRune(b []byte) int {
	for n := 1; n < utf8.UTFMax && n <= len(b); n++ {
		if utf8.RuneStart(b[len(b)-n]) {
			if utf8.FullRune(b[len(b)-n:]) {
				return 0
			}
			return n
		}
	}
	return 0
}

// cleanLines cleans each line of a list from a host, in a new list.
func cleanLines(list []string, clean func(string) string) []string {
	if list == nil {
		return nil
	}
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = clean(s)
	}
	return out
}

// CleanSession is a copy of a session row to draw, with every string that
// the host sent cleaned. Requests use the row as the host sent it.
func CleanSession(s Session) Session {
	s.ID, s.Name, s.Agent = CleanLine(s.ID), CleanLine(s.Name), CleanLine(s.Agent)
	s.Cwd = CleanPath(s.Cwd)
	s.State, s.Source, s.Reason = CleanLine(s.State), CleanLine(s.Source), CleanLine(s.Reason)
	s.Signal, s.Unit, s.HostVersion = CleanLine(s.Signal), CleanLine(s.Unit), CleanLine(s.HostVersion)
	s.MissingSecrets = cleanLines(s.MissingSecrets, CleanLine)
	s.Secrets = cleanLines(s.Secrets, CleanLine)
	return s
}

// CleanSessions is CleanSession for each row of a list, in a new list.
func CleanSessions(list []Session) []Session {
	if list == nil {
		return nil
	}
	out := make([]Session, len(list))
	for i, s := range list {
		out[i] = CleanSession(s)
	}
	return out
}

// ValidSessions drops each row of a host's list whose ID is not a session
// ID (api.ValidSessionID), and notes it in the debug log. Every helper
// makes IDs of that form, so such a row comes from a broken or a hostile
// host, and no request could name it safely.
func ValidSessions(host string, list []Session) []Session {
	var out []Session
	for _, s := range list {
		if !api.ValidSessionID(s.ID) {
			DebugLog().Warn("dropped a session whose ID is not a session ID", "host", host, "id", strconv.QuoteToASCII(s.ID))
			continue
		}
		out = append(out, s)
	}
	if out == nil && list != nil {
		out = []Session{}
	}
	return out
}

// CleanChecks cleans a host's checks, in a new list.
func CleanChecks(list []Check) []Check {
	if list == nil {
		return nil
	}
	out := make([]Check, len(list))
	for i, c := range list {
		out[i] = Check{Name: CleanLine(c.Name), OK: c.OK, Detail: CleanLine(c.Detail), Fix: CleanLine(c.Fix)}
	}
	return out
}

// CleanAgentChecks cleans a host's agent checks, in a new list.
func CleanAgentChecks(list []AgentCheck) []AgentCheck {
	if list == nil {
		return nil
	}
	out := make([]AgentCheck, len(list))
	for i, c := range list {
		c.Kind, c.Title, c.Support = CleanLine(c.Kind), CleanLine(c.Title), CleanLine(c.Support)
		c.Path, c.Login, c.LoginHow = CleanPath(c.Path), CleanLine(c.Login), CleanLine(c.LoginHow)
		out[i] = c
	}
	return out
}

// cleanMap cleans the keys and the values of a map from a host, in a new map.
func cleanMap(m map[string]string, value func(string) string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[CleanLine(k)] = value(v)
	}
	return out
}

// CleanHostInfo cleans every string of a host's doctor report, to draw.
func CleanHostInfo(i HostInfo) HostInfo {
	i.Hostname, i.OS, i.Arch = CleanLine(i.Hostname), CleanLine(i.OS), CleanLine(i.Arch)
	i.Kernel, i.Distro, i.Version, i.User = CleanLine(i.Kernel), CleanLine(i.Distro), CleanLine(i.Version), CleanLine(i.User)
	i.Agents = cleanMap(i.Agents, CleanPath)
	i.AgentVersions = cleanMap(i.AgentVersions, CleanLine)
	i.Checks = CleanChecks(i.Checks)
	i.AgentChecks = CleanAgentChecks(i.AgentChecks)
	return i
}

// OfferFolders keeps the folders that a host suggests that cleaning leaves
// as they are, and drops the others. A suggestion goes back to the host as
// a session's folder, so it must be the folder that the user sees: a name
// with a hidden character would draw as another folder.
func OfferFolders(r FoldersResult) FoldersResult {
	keep := func(list []string) []string {
		var out []string
		for _, p := range list {
			if CleanPath(p) == p {
				out = append(out, p)
			}
		}
		return out
	}
	return FoldersResult{Matches: keep(r.Matches), Recent: keep(r.Recent), Repos: keep(r.Repos)}
}

// CleanBlock cleans text of one or more lines from a host, to draw: each
// line with CleanLine, and the line breaks kept.
func CleanBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = CleanLine(l)
	}
	return strings.Join(lines, "\n")
}

// tailStderr is the end of what a host wrote to stderr, as an error shows
// it: its last three lines, each one cleaned.
func tailStderr(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return CleanBlock(strings.Join(lines, "\n"))
}

// checkSessionID refuses a session ID in a host's answer that is not one
// (api.ValidSessionID). The client goes on to attach to the session, and
// the ID would reach requests, commands and the terminal's title.
func checkSessionID(host, id string) error {
	if api.ValidSessionID(id) {
		return nil
	}
	return &UserError{Msg: fmt.Sprintf("%s answered with a session ID that is not one: %+q", host, CleanLine(id)),
		Hint: brand.Name + " host add " + host + "   (reinstalls the helper)"}
}

// hostError is the error that a host sent in its answer, cleaned to draw.
// Its code and its data stay as the host sent them, for the client to act
// on. A question in its data is cleaned where it is drawn (ShowPath).
func hostError(e *proto.Error) *UserError {
	return &UserError{Msg: CleanLine(e.Message), Hint: CleanLine(e.Hint), Code: e.Code, Data: e.Data}
}
