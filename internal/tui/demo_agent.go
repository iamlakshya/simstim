package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
)

// The demo's made-up agent, which an attach shows. It fills the whole
// terminal, the way a real session does, and shows what the user types in
// its input line. Nothing connects anywhere.

// The agent's colours: slots of the 16-colour palette, so the user's theme
// decides how they look.
const (
	sgrDim     = "\x1b[2m"
	sgrBold    = "\x1b[1m"
	sgrGreen   = "\x1b[32m"
	sgrRed     = "\x1b[31m"
	sgrCyan    = "\x1b[36m"
	sgrMagenta = "\x1b[35m"
	sgrReset   = "\x1b[0m"
)

// Attach shows a made-up agent screen that fills the whole terminal, the
// way a real session does, until the detach key.
func (b *demoBackend) Attach(ctx context.Context, o client.AttachOptions) (client.AttachResult, error) {
	b.mu.Lock()
	var s client.Session
	for _, x := range b.sessions[o.Host.Name] {
		if x.ID == o.Session {
			s = x
		}
	}
	b.mu.Unlock()
	in, out := o.In, o.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return client.AttachResult{}, fmt.Errorf("put the terminal in raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(in.Fd()), old) }()
	cr, err := cancelreader.NewReader(in)
	if err != nil {
		return client.AttachResult{}, fmt.Errorf("read the terminal: %w", err)
	}
	defer cr.Close()
	keys := readKeys(cr)
	_, _ = io.WriteString(out, "\x1b[?1049h")
	defer func() {
		cr.Cancel()
		_, _ = io.WriteString(out, "\x1b[?1049l")
	}()
	(&demoAgent{s: s, out: out}).run(ctx, keys)
	return client.AttachResult{Detached: true}, nil
}

// readKeys sends each read of r on the channel, and closes it when a read
// fails.
func readKeys(r io.Reader) <-chan []byte {
	keys := make(chan []byte, 8)
	go func() {
		defer close(keys)
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				keys <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	return keys
}

// demoAgent is the made-up agent behind an attach: its session, the line
// the user is typing, and the prompts that Enter sent.
type demoAgent struct {
	s          client.Session
	out        *os.File
	cols, rows int
	input      []rune
	sent       []string
}

// keyChange is what a read of keys changed on the agent's screen.
type keyChange int

const (
	keyNone  keyChange = iota
	keyTyped           // the input line
	keySent            // the transcript too: Enter sent a prompt
)

// run draws the screen, and draws it again when the terminal's size changes
// or a prompt is sent, until the detach key or the end of ctx. A typed key
// draws only the input line.
func (a *demoAgent) run(ctx context.Context, keys <-chan []byte) {
	a.draw(false)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case k, ok := <-keys:
			// Ctrl+\, as a legacy byte or a kitty keyboard sequence.
			if !ok || bytes.IndexByte(k, 0x1c) >= 0 || bytes.Contains(k, []byte("\x1b[92;5u")) {
				return
			}
			switch a.key(k) {
			case keyTyped:
				_, _ = io.WriteString(a.out, a.inputRow(a.cols, a.rows))
			case keySent:
				a.draw(true)
			case keyNone: // nothing to draw
			}
		case <-tick.C:
			a.draw(false)
		case <-ctx.Done():
			return
		}
	}
}

// draw draws the whole screen when the terminal's size has changed, or
// always with force.
func (a *demoAgent) draw(force bool) {
	c, r, err := term.GetSize(int(a.out.Fd()))
	if err != nil || !force && c == a.cols && r == a.rows {
		return
	}
	a.cols, a.rows = c, r
	_, _ = io.WriteString(a.out, a.screen(c, r))
}

// key reads one read of keys. Text goes in the input line, Backspace takes
// back the last character, and Enter sends the line as a prompt. Other
// control keys change nothing, and neither does an escape sequence, such as
// an arrow key, or anything after it.
func (a *demoAgent) key(k []byte) keyChange {
	if i := bytes.IndexByte(k, 0x1b); i >= 0 {
		k = k[:i]
	}
	change := keyNone
	for _, r := range string(k) {
		switch {
		case (r == '\r' || r == '\n') && len(a.input) > 0:
			a.sent = append(a.sent, string(a.input))
			a.input = nil
			change = keySent
		case (r == 0x7f || r == 0x08) && len(a.input) > 0:
			a.input = a.input[:len(a.input)-1]
			change = max(change, keyTyped)
		case r != utf8.RuneError && unicode.IsPrint(r):
			a.input = append(a.input, r)
			change = max(change, keyTyped)
		}
	}
	return change
}

// demoScreen is a made-up agent's screen, cols by rows, as its own bytes:
// a transcript at the top and a prompt box at the bottom, edge to edge.
func demoScreen(s client.Session, cols, rows int) string {
	return (&demoAgent{s: s}).screen(cols, rows)
}

// screen is the agent's whole screen, cols by rows, as its own bytes.
func (a *demoAgent) screen(cols, rows int) string {
	talk := demoTalk(a.s, a.sent)
	if rows < 6 || cols < 20 {
		return "\x1b[H\x1b[2J" + strings.Join(talk, "\r\n")
	}
	room := rows - 5 // a blank first row, and the prompt box with its status line
	if len(talk) > room {
		talk = talk[len(talk)-room:]
	}
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	at := func(row int, text string) { fmt.Fprintf(&b, "\x1b[%d;1H%s", row, text) }
	for i, l := range talk {
		at(2+i, " "+l)
	}
	line := strings.Repeat("─", cols-2)
	at(rows-3, sgrDim+"╭"+line+"╮"+sgrReset)
	at(rows-1, sgrDim+"╰"+line+"╯"+sgrReset)
	where := strings.Replace(a.s.Cwd, "/home/me", "~", 1) + " · " + a.s.Agent
	left := " ? for shortcuts"
	gap := max(cols-len(left)-len([]rune(where))-1, 1)
	at(rows, sgrDim+left+strings.Repeat(" ", gap)+where+" "+sgrReset)
	b.WriteString(a.inputRow(cols, rows))
	return b.String()
}

// inputRow draws the prompt box's input line with what the user has typed,
// and leaves the cursor after the text. A line wider than the box shows its
// end.
func (a *demoAgent) inputRow(cols, rows int) string {
	if rows < 6 || cols < 20 {
		return ""
	}
	room := cols - 6 // the borders, " > ", and a cell for the cursor
	text := a.input
	for ansi.StringWidth(string(text)) > room {
		text = text[1:]
	}
	w := ansi.StringWidth(string(text))
	return fmt.Sprintf("\x1b[%d;1H%s│%s > %s%s%s│%s\x1b[%d;%dH", rows-2, sgrDim, sgrReset,
		string(text), strings.Repeat(" ", cols-5-w), sgrDim, sgrReset, rows-2, 5+w)
}

// demoTalk is the transcript of a made-up agent in the session's state,
// then each prompt sent while attached, and the agent at work on them.
func demoTalk(s client.Session, sent []string) []string {
	var talk []string
	switch s.State {
	case "needs_input":
		talk = []string{
			sgrBold + "> " + sgrReset + "Move the session store to the new token format, and keep old tokens working.",
			"",
			sgrMagenta + "● " + sgrReset + "I'll read the current store first.",
			sgrDim + "  Read internal/auth/store.go (212 lines)" + sgrReset,
			sgrDim + "  Read internal/auth/token.go (96 lines)" + sgrReset,
			"",
			sgrMagenta + "● " + sgrReset + "Plan: add a v2 encoder, read both formats, migrate on write.",
			"",
			sgrMagenta + "● " + sgrReset + "Edit internal/auth/token.go",
			sgrGreen + "  + func encodeV2(t Token) ([]byte, error) {" + sgrReset,
			sgrGreen + "  +     return json.Marshal(tokenV2{ID: t.ID, Exp: t.Exp})" + sgrReset,
			sgrRed + "  - return legacyEncode(t)" + sgrReset,
			"",
			sgrBold + "  Run the migration tests?" + sgrReset,
			"  go test ./internal/auth/... -run Migrate",
			"",
			sgrCyan + "  ❯ 1. Yes" + sgrReset,
			"    2. No, and tell the agent what to do differently",
		}
	case "working":
		talk = []string{
			sgrBold + "> " + sgrReset + "Make the API tests pass on the new schema.",
			"",
			sgrDim + "  ran go test ./... (3 failed)" + sgrReset,
			"  fixing handlers/users.go: the nullable email column",
			"",
			sgrCyan + "∴ Working (14s · esc to interrupt)" + sgrReset,
		}
	case "errored":
		// The last screen before the memory cap stopped the agent.
		talk = []string{
			sgrBold + "> " + sgrReset + "Import the full data set, then check the totals.",
			"",
			sgrMagenta + "● " + sgrReset + "Loading all 40 files at once to compare the totals.",
			sgrDim + "  ⎿ Read 31 of 40 files (2.1 GB)" + sgrReset,
			"",
			sgrCyan + "∴ Working (3m 12s · esc to interrupt)" + sgrReset,
		}
	default:
		talk = []string{sgrDim + "  (idle)" + sgrReset}
	}
	for _, p := range sent {
		talk = append(talk, "", sgrBold+"> "+sgrReset+p)
	}
	if len(sent) > 0 {
		talk = append(talk, "", sgrCyan+"∴ Working (esc to interrupt)"+sgrReset)
	}
	return talk
}
