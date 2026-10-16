// Package termtest makes terminal output for tests and benchmarks. Only
// tests import it.
package termtest

import (
	"bytes"
	"fmt"
)

// words are the text that the generators draw.
var words = []string{"Reading", "internal/host/holder.go", "and", "the", "tests", "✓", "passed", "─────", "日本語", "→"}

// AgentOutput is about n bytes of output shaped like a coding agent's
// full-screen redraws: synchronized updates, cursor moves, line erases,
// truecolour and bold text, box drawing, wide characters and OSC 8 links.
// It is the same for the same n.
func AgentOutput(n int) []byte {
	var b bytes.Buffer
	for frame := 0; b.Len() < n; frame++ {
		b.WriteString("\x1b[?2026h\x1b[H")
		for row := 1; row <= 24 && b.Len() < n; row++ {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K", row)
			r, g, bl := (frame*7+row*13)%256, (row*29)%256, (frame*31)%256
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", r, g, bl)
			if row%5 == 0 {
				b.WriteString("\x1b[1m")
			}
			for w := range 9 {
				b.WriteString(words[(frame+row+w)%len(words)])
				b.WriteByte(' ')
			}
			if row%11 == 0 {
				b.WriteString("\x1b]8;;https://example.com/docs\x1b\\link\x1b]8;;\x1b\\")
			}
			b.WriteString("\x1b[0m")
		}
		b.WriteString("\x1b[24;3H\x1b[?2026l")
	}
	return b.Bytes()
}

// InlineOutput is about n bytes of output shaped like an agent that draws
// inline, on the terminal's normal screen, while it prints its transcript:
// lines of truecolour and bold text, box drawing and wide characters, each
// ended by CR LF. Each line is 70 columns wide at most, so on a screen of 80
// columns or more, every line scrolls the screen up by one. It is the same
// for the same n.
func InlineOutput(n int) []byte {
	var b bytes.Buffer
	for line := 0; b.Len() < n; line++ {
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", (line*7)%256, (line*29)%256, (line*31)%256)
		if line%5 == 0 {
			b.WriteString("\x1b[1m")
		}
		for w := range 5 + line%5 {
			b.WriteString(words[(line+w)%len(words)])
			b.WriteByte(' ')
		}
		b.WriteString("\x1b[0m\r\n")
	}
	return b.Bytes()
}
