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

// stringKeys is a terminal that sends s.
type stringKeys struct{ *strings.Reader }

func (stringKeys) Cancel() bool { return true }
func (stringKeys) Close() error { return nil }

// feedAll gives the editor the keys of s, as the terminal reader reads
// them, and stops at the end of the line or a cancel.
func feedAll(e *lineEditor, s string) (echo string, done, cancel bool) {
	keys := keyread.New(stringKeys{strings.NewReader(s)})
	var b strings.Builder
	for {
		k, err := keys.ReadKey(context.Background())
		if err != nil {
			return b.String(), false, false
		}
		out, d, c := e.key(k)
		b.WriteString(out)
		if d || c {
			return b.String(), d, c
		}
	}
}

func TestLineEditor(t *testing.T) {
	var e lineEditor
	echo, done, cancel := feedAll(&e, "héllo\x7f\x7fp\x1b[D\x1bOA\x1b[1;5C")
	if done || cancel || string(e.line) != "hélp" || echo != "héllo\b \b\b \bp" {
		t.Fatalf("line %q, echo %q, done %v, cancel %v", string(e.line), echo, done, cancel)
	}
	if echo, done, _ = feedAll(&e, "\x15ok\r"); !done || string(e.line) != "ok" || !strings.HasPrefix(echo, "\b \b\b \b\b \b\b \b") {
		t.Fatalf("line %q, echo %q, done %v", string(e.line), echo, done)
	}
	e = lineEditor{}
	if _, done, _ = feedAll(&e, "rm -rf  \x17now\r"); !done || string(e.line) != "rm now" {
		t.Fatalf("ctrl+w: line %q", string(e.line))
	}
	e = lineEditor{}
	if _, _, cancel = feedAll(&e, "\x04"); !cancel {
		t.Error("ctrl+d on an empty line does not cancel")
	}
	e = lineEditor{line: []rune("a")}
	if _, _, cancel = feedAll(&e, "\x04"); cancel {
		t.Error("ctrl+d after text cancels")
	}
	e = lineEditor{}
	if _, _, cancel = feedAll(&e, "\x03"); !cancel {
		t.Error("ctrl+c does not cancel")
	}
	// A wide rune takes two cells, and backspace erases both.
	e = lineEditor{}
	if echo, _, _ = feedAll(&e, "世\x7f"); echo != "世\b\b  \b\b" || len(e.line) != 0 {
		t.Errorf("a wide rune: echo %q, line %q", echo, string(e.line))
	}
	// Bytes that cannot make a rune are dropped.
	e = lineEditor{}
	if feedAll(&e, "\xff\xfea\r"); string(e.line) != "a" {
		t.Errorf("bad bytes: line %q", string(e.line))
	}
}

// Ctrl+c, ctrl+d and enter come before a rune or a sequence that is not
// whole: they end it, and do what they do.
func TestControlsEndAPartialKey(t *testing.T) {
	for _, c := range []struct {
		in           string
		line         string
		done, cancel bool
	}{
		{"ab\xf0\x03", "ab", false, true},
		{"ab\x1b[\x03", "ab", false, true},
		{"ab\xc3\r", "ab", true, false},
		{"ab\x1b[1;\r", "ab", true, false},
		{"\xe2\x82\x04", "", false, true},
	} {
		var e lineEditor
		_, done, cancel := feedAll(&e, c.in)
		if done != c.done || cancel != c.cancel || string(e.line) != c.line {
			t.Errorf("%q: line %q, done %v, cancel %v", c.in, string(e.line), done, cancel)
		}
	}
}

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

type asked struct {
	text string
	err  error
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

// A question turns bracketed paste on everywhere but on Windows, whose
// console may show the sequences as text.
func TestBracketedPasteIsOnOutsideWindows(t *testing.T) {
	if pasteModes != (runtime.GOOS != "windows") {
		t.Errorf("bracketed paste at questions on %s: %v", runtime.GOOS, pasteModes)
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

// After the third answer that is not one, the question cancels without a
// fourth hint.
func TestThirdBadAnswerCancelsAtOnce(t *testing.T) {
	var out strings.Builder
	p := &prompter{ctx: context.Background(), in: strings.NewReader("x\nmaybe\n?\n"), out: &out, on: true}
	if _, err := p.yesNo("Go on?", false); !isCancel(err) {
		t.Fatalf("err = %v", err)
	}
	if n := strings.Count(out.String(), "Type y or n"); n != 2 {
		t.Errorf("the hint showed %d times, want 2:\n%s", n, out.String())
	}
}
