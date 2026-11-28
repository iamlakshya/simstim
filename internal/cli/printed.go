package cli

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Printed commands (docs/contributing/cli-spec.md 9.2). A command that the
// CLI prints for a person, in a Try: line, a hint, a note or the attach
// line of show, names a session that the CLI found by host/ID, never by
// its name: an ID means one session on its host, and a name can mean
// another. The name goes in the message's text, cleaned. A printed command
// reaches the terminal whole, or it is not printed: a word that cleaning
// changes, or a line that the cut at 4000 runes shortens, could read as
// another session. lsOf then stands in for it.

// idAddress is the address of the session id on host, as a printed command
// holds it, and whether it may go there: a shell reads it back as it is,
// with nothing cleaned or cut.
func idAddress(host, id string) (string, bool) {
	addr := host + "/" + id
	return addr, shellWord(addr) == addr
}

// whole reports whether the terminal shows line as it is: cleaning removes
// nothing from it, and the cut at 4000 runes leaves it whole.
func whole(line string) bool { return shown(line) == line }

// lsOf is the command that lists the sessions of host, or of every host
// when the host's name could not go in a command. It names no session, so
// it stands in for a printed command that would not reach the terminal
// whole.
func lsOf(host string) string {
	if client.ValidHostName(host) {
		return brand.Name + " ls " + host
	}
	return brand.Name + " ls"
}

// newHostHint names a saved host only when the command reaches the shell whole.
func newHostHint(host string) string {
	if client.ValidHostName(host) {
		return printedOr(host, brand.Name+" new "+shellWord(host))
	}
	return brand.Name + " host add <name>"
}

// hostStartHint never turns an invalid saved name into a shell command.
func hostStartHint(host string) string {
	if client.ValidHostName(host) {
		return printedOr(host, brand.Name+" host start "+shellWord(host))
	}
	return lsOf(host)
}

// printedOr is line, a printed command for a session on host, when it
// reaches the terminal whole, or else lsOf(host).
func printedOr(host, line string) string {
	if whole(line) {
		return line
	}
	return lsOf(host)
}

// sessionCommand is the printed command path, such as "debug start", for
// the session id on host: the path, host/ID, then the words after. It is
// lsOf(host) when the address could not go in a command, or the line would
// not reach the terminal whole or would not run.
func sessionCommand(host, id, path string, after ...string) string {
	addr, ok := idAddress(host, id)
	if !ok {
		return lsOf(host)
	}
	words := append(append(strings.Fields(path), addr), after...)
	if !parses(words) {
		return lsOf(host)
	}
	return printedOr(host, brand.Name+" "+joinWords(words))
}

// tryAddress is the Try: line that names the session id of host: the
// command line, in its new form, with host/ID in place of the address
// that the user typed, or after the command's path when the line holds
// none, as after the picker. When a word of that line would not reach the
// terminal whole, or the line would not run, it is the command's path and
// the address alone.
func tryAddress(cmd *cobra.Command, host, id string) string {
	addr, ok := idAddress(host, id)
	switch {
	case !ok:
		return lsOf(host)
	case cmd == nil:
		return printedOr(host, brand.Name+" attach "+addr)
	}
	if line := sessionLine(cmd, addr); line != nil && parses(line) {
		return printedOr(host, brand.Name+" "+joinWords(line))
	}
	return printedOr(host, brand.Name+" "+newPath(cmd)+" "+addr)
}

// sessionLine is the command line of cmd, in its new form, with addr in
// place of the address that the user typed, or after the command's path
// when the line holds none. It is nil when a word of the line would not
// reach the terminal whole, or the line holds no path.
func sessionLine(cmd *cobra.Command, addr string) []string {
	line := commandLine(cmd)
	at := plain(cmd, line)
	n := pathLen(cmd)
	if len(at) < n || n == 0 {
		return nil
	}
	var fixed []string
	if len(at) > n {
		fixed = slices.Clone(line)
		fixed[at[n]] = addr
	} else {
		fixed = slices.Insert(slices.Clone(line), at[n-1]+1, addr)
	}
	for _, w := range fixed {
		if !whole(w) {
			return nil
		}
	}
	return newForm(cmd, fixed)
}

// sessionPrompts is the prompter of cmd for a question about the session
// id on host. The Try: line of a question that input off leaves without an
// answer is the command line of sessionLine, with the flag that answers.
func sessionPrompts(cmd *cobra.Command, host, id string) *prompter {
	p := prompts(cmd)
	addr, ok := idAddress(host, id)
	if !ok {
		p.try = lsOf(host)
		return p
	}
	p.short = append(strings.Fields(newPath(cmd)), addr)
	p.host = host
	line := sessionLine(cmd, addr)
	if line == nil {
		line = p.short
	}
	p.line, p.flagsAt = line, flagsAt(cmd, line)
	return p
}

// typedAddress is the address that the user typed, when a printed command
// may repeat it before any lookup: "-", or host/ID with a host's name and
// an ID that the helper makes. Any other word could mean another session
// once cleaned, so it is "<host>/<ID>".
func typedAddress(word string) string {
	if meansOneSession(word) {
		return word
	}
	return "<host>/<ID>"
}

// meansOneSession reports whether word, an address that no lookup found,
// means one session as it is: "-", the session last attached to by its
// ID, or host/ID with a host's name and a word of the form of an ID.
func meansOneSession(word string) bool {
	h, id, ok := strings.Cut(word, "/")
	return word == "-" || ok && client.ValidHostName(h) && api.LooksLikeID(id)
}

// typedLine is the Try: line of words, a command line that repeats what
// the user typed before any lookup, and line, those words as the line
// prints them. Every such Try: line comes from here: a parser's suggestion
// (cli-spec 4.3), and the line of a question or of --json without
// --detach (prompt.go). It is line when the terminal shows it whole and
// it names no session that no lookup found (cli-spec 9.2). When the
// command takes a session, and the line names it other than by host/ID or
// "-", it is the list of the host's sessions. When a word or the line
// would not reach the terminal whole, it is fallback, such as the help of
// the command.
func typedLine(words []string, line, fallback string) string {
	if !whole(line) {
		return fallback
	}
	for _, w := range words {
		if !whole(w) {
			return fallback
		}
	}
	c, rest, err := Root().Find(words)
	if err != nil || !strings.Contains(c.Use, "<host>/<session>") {
		return line
	}
	// The flags of its parents, which plain reads, join the command's.
	_ = c.ParseFlags(rest)
	at := plain(c, words)
	if n := pathLen(c); len(at) > n && !meansOneSession(words[at[n]]) {
		h, _, hasHost := strings.Cut(words[at[n]], "/")
		// debug bundle takes a host alone too: a word without "/" is a host.
		if hasHost || !strings.Contains(c.Use, "<host> |") {
			if !hasHost {
				h = ""
			}
			return lsOf(h)
		}
	}
	return line
}

// suggestedLine is the Try: line of a parser's suggestion: fixed, the
// command line that the user typed with one word fixed, through typedLine,
// or fallback.
func suggestedLine(fixed []string, fallback string) string {
	return typedLine(fixed, brand.Name+" "+joinWords(fixed), fallback)
}
