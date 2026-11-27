//go:build unix

package cli

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

// askOn asks one free-text question on a terminal, and returns the
// answer.
func askOn(t *testing.T, term *hosttest.Terminal) chan asked {
	t.Helper()
	got := make(chan asked, 1)
	go func() {
		p := &prompter{ctx: context.Background(), in: term.TTY, out: term.TTY, on: true}
		a, err := p.ask("Name? ")
		got <- asked{a, err}
	}()
	return got
}
func waitAnswer(t *testing.T, term *hosttest.Terminal, got chan asked) asked {
	t.Helper()
	select {
	case a := <-got:
		return a
	case <-time.After(10 * time.Second):
		t.Fatalf("the question never ended; the terminal shows %q", term.Output())
		return asked{}
	}
}

// A key that sends a sequence, or a rune of more than one byte, at a
// question on a terminal: a rune is text, and an arrow does nothing, and
// never cancels. A key cut across two reads, which depends on time, is
// keyread's test, with a clock of its own (TestASplitKeyIsOneKey); here
// each key comes whole.
func TestWholeKeysAtAQuestion(t *testing.T) {
	for _, c := range []struct{ key, want string }{
		{"\x1b[1;5C", "y"},
		{"\x1bOA", "y"},
		{"𝄞", "𝄞y"},
		{"é", "éy"},
		{"\x1b[200~a\nb\x1b[201~", "aby"},
	} {
		term := hosttest.NewTerminal(t, 80, 10)
		got := askOn(t, term)
		term.WaitFor("Name? ")
		term.Type(c.key + "y\r")
		if a := waitAnswer(t, term, got); a.err != nil || a.text != c.want {
			t.Errorf("%q: %q, %v, want %q", c.key, a.text, a.err, c.want)
		}
	}
	// esc alone cancels once the wait runs out.
	term := hosttest.NewTerminal(t, 80, 10)
	got := askOn(t, term)
	term.WaitFor("Name? ")
	start := time.Now()
	term.Type("\x1b")
	if a := waitAnswer(t, term, got); !isCancel(a.err) || time.Since(start) < keyread.EscWait {
		t.Errorf("a lone esc: %v after %s", a.err, time.Since(start))
	}
}

// A paste at a question: one of a megabyte is the answer, whole. The
// reader's speed is keyread's test (TestABigPasteIsQuick): here the echo of
// the megabyte to the terminal takes its own time. Ctrl+C in a paste whose
// end never comes cancels the question.
func TestAPasteAtAQuestion(t *testing.T) {
	term := hosttest.NewTerminal(t, 80, 10)
	got := askOn(t, term)
	term.WaitFor("Name? ")
	big := strings.Repeat("a", keyread.MaxPaste)
	term.Type("\x1b[200~" + big + "\x1b[201~\r")
	if a := waitAnswer(t, term, got); a.err != nil || a.text != big {
		t.Errorf("a paste of %d bytes: %d bytes, %v", len(big), len(a.text), a.err)
	}

	term = hosttest.NewTerminal(t, 80, 10)
	got = askOn(t, term)
	term.WaitFor("Name? ")
	term.Type("\x1b[200~half a paste\x03")
	if a := waitAnswer(t, term, got); !isCancel(a.err) {
		t.Errorf("ctrl+c in a paste: %q, %v, want a cancel", a.text, a.err)
	}
}

// A question turns bracketed paste on while it reads, and off before it
// restores the terminal: at an answer, at a cancel, and at the end of its
// context, as at a signal. So a paste of two lines is text in the line,
// and the enter after it gives the answer.
func TestAQuestionTurnsBracketedPasteOn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a question sends no bracketed paste mode on Windows")
	}
	t.Setenv("TERM", "xterm-256color") // not dumb
	old := promptStage
	t.Cleanup(func() { promptStage = old })
	for _, c := range []struct {
		keys, then string
		answer     string
		end        bool // the context ends once the keys are in
	}{
		{"\x1b[200~first line\nsecond line\x1b[201~", "\r", "first linesecond line", false},
		{"\x1b[200~half a paste\x03", "", "", false},
		{"half a line", "", "", true},
	} {
		term := hosttest.NewTerminal(t, 80, 10)
		var sent lockedBuffer
		atRestore := make(chan string, 1)
		promptStage = func(stage string) {
			if stage == "restored" {
				atRestore <- sent.String()
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		got := make(chan asked, 1)
		go func() {
			// sent gets each write first, so it holds what the terminal shows.
			p := &prompter{ctx: ctx, in: term.TTY, out: io.MultiWriter(&sent, term.TTY), on: true}
			a, err := p.ask("Name? ")
			got <- asked{a, err}
		}()
		term.WaitFor("Name? ")
		if s := sent.String(); !strings.HasPrefix(s, pasteOn+"Name? ") {
			t.Errorf("before the question: %q, want bracketed paste on first", s)
		}
		term.Type(c.keys)
		if c.then != "" {
			term.WaitFor("second line")
			select {
			case a := <-got:
				t.Fatalf("a paste of two lines ended the question: %q, %v", a.text, a.err)
			case <-time.After(100 * time.Millisecond):
			}
			term.Type(c.then)
		}
		if c.end {
			term.WaitFor(c.keys)
			cancel()
		}
		a := waitAnswer(t, term, got)
		if c.answer != "" && (a.err != nil || a.text != c.answer) || c.answer == "" && !isCancel(a.err) {
			t.Errorf("%q: %q, %v", c.keys, a.text, a.err)
		}
		if s := <-atRestore; !strings.HasSuffix(s, pasteOff) || strings.Count(s, pasteOff) != 1 {
			t.Errorf("%q: at the restore the question had sent %q, want bracketed paste off last", c.keys, s)
		}
	}
}

// At TERM=dumb, which may show escape sequences as text, a question sends
// no bracketed paste mode, and still reads its answer.
func TestNoBracketedPasteAtADumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	term := hosttest.NewTerminal(t, 80, 10)
	var sent lockedBuffer
	got := make(chan asked, 1)
	go func() {
		p := &prompter{ctx: context.Background(), in: term.TTY, out: io.MultiWriter(&sent, term.TTY), on: true}
		a, err := p.ask("Name? ")
		got <- asked{a, err}
	}()
	term.WaitFor("Name? ")
	term.Type("box\r")
	if a := waitAnswer(t, term, got); a.err != nil || a.text != "box" {
		t.Errorf("an answer at TERM=dumb: %q, %v", a.text, a.err)
	}
	if s := sent.String(); strings.Contains(s, "\x1b[?2004") {
		t.Errorf("a question at TERM=dumb sent %q", s)
	}
}

// The reader never reads past the end of the line: an answer and what the
// user typed after it can come in one read, and the rest stays for the
// next question, unchanged.
func TestBytesAfterEnterStay(t *testing.T) {
	term := hosttest.NewTerminal(t, 80, 10)
	got := make(chan []string, 1)
	go func() {
		p := &prompter{ctx: context.Background(), in: term.TTY, out: term.TTY, on: true}
		var answers []string
		for range 3 {
			a, err := p.ask("Q? ")
			if err != nil {
				answers = append(answers, err.Error())
				break
			}
			answers = append(answers, a)
		}
		got <- answers
	}()
	term.WaitFor("Q? ")
	term.Type("first\rsécond 𝄞\r\x1b[Athird\r")
	select {
	case answers := <-got:
		if strings.Join(answers, "|") != "first|sécond 𝄞|third" {
			t.Fatalf("answers = %q", answers)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the questions never ended; the terminal shows %q", term.Output())
	}
}
