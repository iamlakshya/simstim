package chv

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestVMMLogIsBoundedAndDisplayExcerptIsClean(t *testing.T) {
	var tail byteTail
	if n, err := tail.Write(bytes.Repeat([]byte("x"), 2*vmmLogBytes)); err != nil || n != 2*vmmLogBytes {
		t.Fatal(n, err)
	}
	_, _ = tail.Write([]byte("\nlast\x1b[31m\x00\u202e diagnostic\r\n"))
	if len(tail.data) != vmmLogBytes {
		t.Fatalf("guest-controlled diagnostics retained %d bytes", len(tail.data))
	}
	line := tail.lastLine()
	if strings.ContainsAny(line, "\x1b\x00\r\n") || strings.ContainsRune(line, '\u202e') || api.CleanText(line, 512) != line {
		t.Fatalf("unsafe diagnostic excerpt: %q", line)
	}
}
