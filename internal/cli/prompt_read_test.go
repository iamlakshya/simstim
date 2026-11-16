package cli

import (
	"context"
	"runtime"
	"strings"
	"testing"

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

type asked struct {
	text string
	err  error
}

// A question turns bracketed paste on everywhere but on Windows, whose
// console may show the sequences as text.
func TestBracketedPasteIsOnOutsideWindows(t *testing.T) {
	if pasteModes != (runtime.GOOS != "windows") {
		t.Errorf("bracketed paste at questions on %s: %v", runtime.GOOS, pasteModes)
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
