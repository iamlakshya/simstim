package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keys"
)

// probeQueries asks the terminal about everything the pass-through relies on.
// DA1 goes last: every terminal answers it, so its reply marks the end.
var probeQueries = strings.Join([]string{
	"\x1b[>0q",        // XTVERSION: name and version
	"\x1b[?u",         // kitty keyboard protocol flags
	"\x1b[?2026$p",    // synchronized output
	"\x1b[?2027$p",    // grapheme clustering
	"\x1b[?2031$p",    // colour-scheme change reports
	"\x1b[?2004$p",    // bracketed paste
	"\x1b[?1004$p",    // focus reports
	"\x1b]10;?\x1b\\", // foreground colour
	"\x1b]11;?\x1b\\", // background colour
	"\x1b[c",          // DA1, the end marker
}, "")

type capability struct {
	name, detail string
	ok           bool
}

// terminalProbe asks the terminal what it supports. A signal ends its wait
// for the answers.
func terminalProbe(ctx context.Context) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return &client.UserError{Msg: "the terminal check needs a terminal", Hint: "Run it in your terminal.", Code: "no_terminal"}
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("put the terminal in raw mode: %w", err)
	}
	_, _ = os.Stdout.WriteString(probeQueries)
	reply := readUntilDA1(ctx, 2*time.Second)
	_ = term.Restore(fd, old)
	if ctx.Err() != nil {
		return fmt.Errorf("check the terminal: %w", ctx.Err())
	}

	r := string(reply)
	mode := func(n int) (string, bool) {
		m := regexp.MustCompile(fmt.Sprintf(`\x1b\[\?%d;(\d)\$y`, n)).FindStringSubmatch(r)
		if m == nil {
			return "no answer", false
		}
		switch m[1] {
		case "1", "2":
			return "supported", true
		case "3", "4":
			return "always on or off", true
		}
		return "not supported", false
	}
	var caps []capability
	if m := regexp.MustCompile(`\x1bP>\|([^\x1b]*)\x1b\\`).FindStringSubmatch(r); m != nil {
		caps = append(caps, capability{"terminal", m[1], true})
	} else {
		caps = append(caps, capability{"terminal", os.Getenv("TERM_PROGRAM") + " (no XTVERSION answer)", true})
	}
	if regexp.MustCompile(`\x1b\[\?\d+u`).MatchString(r) {
		caps = append(caps, capability{"kitty keyboard protocol", "supported: Shift+Enter and every key reach the agent", true})
	} else {
		caps = append(caps, capability{"kitty keyboard protocol", "not supported: agents fall back to legacy keys (Shift+Enter may act as Enter)", false})
	}
	for _, x := range []struct {
		n    int
		name string
	}{{2026, "synchronized output"}, {2027, "grapheme clustering"}, {2031, "colour-scheme reports"}, {2004, "bracketed paste"}, {1004, "focus reports"}} {
		d, ok := mode(x.n)
		caps = append(caps, capability{x.name, d, ok})
	}
	bg := regexp.MustCompile(`\x1b\]11;rgb:([0-9a-fA-F/]+)`).FindStringSubmatch(r)
	if bg != nil {
		caps = append(caps, capability{"background colour", "rgb:" + bg[1], true})
	} else {
		caps = append(caps, capability{"background colour", "no answer (light or dark cannot be detected)", false})
	}
	ct := os.Getenv("COLORTERM")
	caps = append(caps, capability{"truecolour", "COLORTERM=" + ct, ct == "truecolor" || ct == "24bit"})
	caps = append(caps, capability{"TERM", os.Getenv("TERM"), os.Getenv("TERM") != ""})

	fmt.Printf("What this terminal supports. %s passes all of it through to agents unchanged.\n\n", brand.DisplayName)
	for _, c := range caps {
		mark := "✓"
		if !c.ok {
			mark = "✕"
		}
		fmt.Printf("  %s %-26s %s\n", mark, c.name, c.detail)
	}
	return nil
}

// readUntilDA1 reads the terminal's replies until the DA1 answer, the
// timeout, the end of ctx or a signal.
func readUntilDA1(ctx context.Context, timeout time.Duration) []byte {
	in, done := inputOf(os.Stdin)
	defer done()
	sctx, cancel := stopping(ctx)
	defer cancel()
	tctx, stop := context.WithTimeout(sctx, timeout)
	defer stop()
	var buf bytes.Buffer
	da1 := regexp.MustCompile(`\x1b\[\?[\d;]*c`)
	err := keys.ReadUntil(tctx, in, func(b []byte) bool {
		buf.Write(b)
		return !da1.Match(buf.Bytes())
	})
	if err != nil && !da1.Match(buf.Bytes()) {
		return nil
	}
	return buf.Bytes()
}
