package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/vt"
)

// A row that stays the same across frames is drawn once, with one span; a
// row that changes gets a group for each look it has.
func TestAnimatedSVGKeepsEachRowOnce(t *testing.T) {
	frames := []frame{
		{at: 0, rows: []string{"<text>title</text>\n", "<text>a</text>\n"}},
		{at: time.Second, rows: []string{"<text>title</text>\n", "<text>b</text>\n"}},
		{at: 2 * time.Second, rows: []string{"<text>title</text>\n", "<text>a</text>\n"}},
	}
	svg := animatedSVG(frames, 4*time.Second, 10, 2)
	if n := strings.Count(svg, "<text>title</text>"); n != 1 {
		t.Errorf("the unchanged row is drawn %d times, want 1", n)
	}
	if n := strings.Count(svg, "<text>a</text>"); n != 1 {
		t.Errorf("the row that comes back is drawn %d times, want 1", n)
	}
	// "a" shows from 0 to 25 %, hides, and shows again from 50 % to the end.
	if !strings.Contains(svg, "0.00%{opacity:1}25.00%{opacity:0}50.00%{opacity:1}") {
		t.Errorf("wrong spans for the row that comes back:\n%s", svg)
	}
	if !strings.Contains(svg, "prefers-reduced-motion") || !strings.Contains(svg, `class="p p0 l"`) {
		t.Error("reduced motion does not show the last frame")
	}
}

func TestWriteCast(t *testing.T) {
	path := t.TempDir() + "/x.cast"
	if err := writeCast(path, []castEvent{{at: 1500 * time.Millisecond, data: "hi\r\n"}}, 80, 24); err != nil {
		t.Fatal(err)
	}
}

// splitText has characters of two, three and four bytes, and box drawing
// like the agent's prompt box.
const splitText = "╭─╮ é 🔒 ─\r\n"

// splits cuts splitText at every byte: each cut alone, and every byte on
// its own, as reads from a pseudo-terminal can split it.
func splits() [][]string {
	var out [][]string
	for i := 1; i < len(splitText); i++ {
		out = append(out, []string{splitText[:i], splitText[i:]})
	}
	var each []string
	for i := range len(splitText) {
		each = append(each, splitText[i:i+1])
	}
	return append(out, each)
}

// A cast's events end on whole characters, however the reads split them,
// and no byte is changed or lost.
func TestWriteCastKeepsSplitCharactersWhole(t *testing.T) {
	for _, reads := range splits() {
		var events []castEvent
		for i, r := range reads {
			events = append(events, castEvent{at: time.Duration(i) * time.Millisecond, data: r})
		}
		path := t.TempDir() + "/x.cast"
		if err := writeCast(path, events, 80, 24); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var joined strings.Builder
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")[1:] {
			var e []any
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			data, _ := e[2].(string)
			if !utf8.ValidString(data) || strings.ContainsRune(data, utf8.RuneError) {
				t.Errorf("reads %q: event %q is not whole UTF-8", reads, data)
			}
			joined.WriteString(data)
		}
		if joined.String() != splitText {
			t.Errorf("reads %q: the events join to %q, want %q", reads, joined.String(), splitText)
		}
	}
}

// A tail that no later event finishes stays in the last event.
func TestWholeCharsKeepsAnUnfinishedTail(t *testing.T) {
	got := wholeChars([]castEvent{{data: "ok"}, {data: "\xe2\x94"}})
	if len(got) != 2 || got[0].data != "ok" || got[1].data != "\xe2\x94" {
		t.Errorf("got %+v", got)
	}
}

// The SVG comes from the emulator's cells, and the emulator joins a split
// character again, so the pictures never had the cast's fault.
func TestSVGKeepsSplitCharactersWhole(t *testing.T) {
	for _, reads := range splits() {
		emu := vt.NewSafeEmulator(20, 2)
		for _, r := range reads {
			_, _ = emu.Write([]byte(r))
		}
		row := svgRows(emu)[0]
		if strings.ContainsRune(row, utf8.RuneError) {
			t.Errorf("reads %q: the row has U+FFFD: %s", reads, row)
		}
		for _, want := range []string{"╭─╮", "é", "🔒", "─"} {
			if !strings.Contains(row, want) {
				t.Errorf("reads %q: the row has no %q: %s", reads, want, row)
			}
		}
	}
}
