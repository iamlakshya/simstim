package host

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/ring"
	"github.com/iamlakshya/simstim/internal/term"
)

// The shadow terminal is built from vt/ with Zig. These tests use the
// binary named by VT_BINARY, or build it with Zig 0.17 when Zig is
// installed, or use one built before. Without any of these they skip. They
// also skip when the binary is built from another Ghostty commit than the
// one that vt/build.zig.zon pins, so no test runs an old shadow terminal.

// vtZig is the Zig version that builds the shadow terminal (vt/build.zig.zon).
const vtZig = "0.17."

var (
	vtBinOnce sync.Once
	vtBinPath string
	vtBinSkip string
	vtBinFail string
)

func vtBinary(t *testing.T) string {
	t.Helper()
	vtBinOnce.Do(findVTBinary)
	if vtBinFail != "" {
		t.Fatal(vtBinFail)
	}
	if vtBinPath == "" {
		t.Skip(vtBinSkip)
	}
	return vtBinPath
}

func findVTBinary() {
	dir, err := filepath.Abs(filepath.Join("..", "..", "vt"))
	if err != nil {
		vtBinSkip = err.Error()
		return
	}
	zig, _ := exec.LookPath("zig")
	vtBinPath, vtBinSkip, vtBinFail = pickVTBinary(dir, os.Getenv("VT_BINARY"), zig)
}

// pickVTBinary picks the shadow terminal that the tests use: env (the value
// of VT_BINARY) when it is set, or else the build in dir/zig-out, which it
// first builds with zig when zig is 0.17. zig is "" when there is none. It
// returns the binary's path, or why the tests skip, or why they fail.
func pickVTBinary(dir, env, zig string) (path, skip, fail string) {
	pin, err := vtPinnedCommit(dir)
	if err != nil {
		return "", "", err.Error()
	}
	if env != "" {
		if got := vtCommitOf(env); got != pin {
			return "", fmt.Sprintf("VT_BINARY %s is built from Ghostty %s, not the pinned %s. "+
				"Try: build it again from vt/ with Zig 0.17.0, or unset VT_BINARY", env, got, pin), ""
		}
		return env, "", ""
	}
	built := filepath.Join(dir, "zig-out", "bin", "shadow-vt")
	have := "no Zig"
	if zig != "" {
		out, err := exec.Command(zig, "version").Output()
		if v := strings.TrimSpace(string(out)); err == nil && v != "" {
			have = v
		}
		if strings.HasPrefix(have, vtZig) {
			// The shipped mode: Debug is far too slow for the long tests.
			cmd := exec.Command(zig, "build", "-Doptimize=ReleaseSafe")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				return "", "", fmt.Sprintf("zig build in vt/ failed: %v\n%s", err, out)
			}
		}
	}
	// The same note as `just vt` gives without the pinned Zig.
	note := fmt.Sprintf("the shadow terminal needs Zig 0.17.0, and this machine has %s.", have)
	try := " Try: just setup, which puts the pinned Zig into .tools/, or set VT_BINARY (see vt/README.md)"
	if _, err := os.Stat(built); err != nil {
		return "", note + try, ""
	}
	if got := vtCommitOf(built); got != pin {
		return "", note + fmt.Sprintf(" The build in vt/zig-out is from Ghostty %s, not the pinned %s.", got, pin) + try, ""
	}
	return built, "", ""
}

// vtPinnedCommit is the Ghostty commit that vt/build.zig.zon in dir pins.
func vtPinnedCommit(dir string) (string, error) {
	zon := filepath.Join(dir, "build.zig.zon")
	b, err := os.ReadFile(zon)
	if err != nil {
		return "", fmt.Errorf("read the shadow terminal's pin: %w", err)
	}
	m := regexp.MustCompile(`ghostty/archive/([0-9a-f]{40})\.tar\.gz`).FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("%s pins no Ghostty commit. Try: zig fetch --save=ghostty <url> in vt/ (see vt/README.md)", zon)
	}
	return string(m[1]), nil
}

// vtCommitOf is the Ghostty commit that the shadow terminal at path says,
// in its --version, that it is built from. It is "an unknown commit" when
// the binary does not say.
func vtCommitOf(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "an unknown commit"
	}
	_, commit, ok := strings.Cut(strings.TrimSpace(string(out)), " ghostty ")
	if !ok || commit == "" {
		return "an unknown commit"
	}
	return commit
}

type vtRecorder struct {
	mu     sync.Mutex
	writes []byte
	events []VTEvent
}

func (r *vtRecorder) onWrite(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, p...)
}

func (r *vtRecorder) onEvent(e VTEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *vtRecorder) get() ([]byte, []VTEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return bytes.Clone(r.writes), append([]VTEvent(nil), r.events...)
}

func vtStartTest(t *testing.T, opt VTOptions) *VT {
	t.Helper()
	if opt.Path == "" {
		opt.Path = vtBinary(t)
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.NewTextHandler(t.Output(), nil))
	}
	v, err := StartVT(opt)
	if err != nil {
		t.Fatalf("StartVT: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

// vtFeedAll feeds data from offset 0 in pieces, as a PTY would deliver it.
func vtFeedAll(v *VT, data []byte) {
	var off int64
	for len(data) > 0 {
		n := min(len(data), 4093)
		v.Feed(off, data[:n])
		off += int64(n)
		data = data[n:]
	}
}

// The dump that the shadow terminal writes for a T frame.
type vtDump struct {
	Cols, Rows int
	Active     string
	Cursor     struct {
		X, Y        int
		Visible     bool
		PendingWrap bool `json:"pending_wrap"`
		Style       string
	}
	Modes           string
	Scroll          []int
	CursorShape     string `json:"cursor_shape"`
	CursorDefault   bool   `json:"cursor_default"`
	ModifyOtherKeys bool   `json:"modify_other_keys"`
	Title, Pwd      string
	Colors          json.RawMessage
	Screens         map[string]vtDumpScreen
}

type vtDumpScreen struct {
	Kitty       []int
	SavedCursor []int `json:"saved_cursor"`
	Scrollback  []vtDumpRow
	Active      []vtDumpRow
}

type vtDumpRow struct {
	Text   string
	Wrap   bool
	Styles json.RawMessage
}

func vtDumpOf(t *testing.T, v *VT, maxLines int) vtDump {
	t.Helper()
	b, err := v.dump(maxLines, 10*time.Second)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	var d vtDump
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("dump is not JSON: %v\n%s", err, b)
	}
	return d
}

// vtCompareDumps checks two terminals cell by cell and reports what differs.
func vtCompareDumps(t *testing.T, want, got vtDump) {
	t.Helper()
	var diffs []string
	add := func(format string, args ...any) { diffs = append(diffs, fmt.Sprintf(format, args...)) }
	if want.Cols != got.Cols || want.Rows != got.Rows {
		add("size: want %dx%d, got %dx%d", want.Cols, want.Rows, got.Cols, got.Rows)
	}
	if want.Active != got.Active {
		add("active screen: want %s, got %s", want.Active, got.Active)
	}
	if want.Cursor != got.Cursor {
		add("cursor: want %+v, got %+v", want.Cursor, got.Cursor)
	}
	if want.Modes != got.Modes {
		add("modes: want %q, got %q", want.Modes, got.Modes)
	}
	if fmt.Sprint(want.Scroll) != fmt.Sprint(got.Scroll) {
		add("scroll region: want %v, got %v", want.Scroll, got.Scroll)
	}
	if want.CursorShape != got.CursorShape || want.CursorDefault != got.CursorDefault {
		add("cursor shape: want %s/%v, got %s/%v", want.CursorShape, want.CursorDefault, got.CursorShape, got.CursorDefault)
	}
	if want.ModifyOtherKeys != got.ModifyOtherKeys {
		add("modifyOtherKeys: want %v, got %v", want.ModifyOtherKeys, got.ModifyOtherKeys)
	}
	if want.Title != got.Title || want.Pwd != got.Pwd {
		add("title and pwd: want %q %q, got %q %q", want.Title, want.Pwd, got.Title, got.Pwd)
	}
	if !bytes.Equal(want.Colors, got.Colors) {
		add("colours: want %s, got %s", want.Colors, got.Colors)
	}
	for name, ws := range want.Screens {
		gs, ok := got.Screens[name]
		if !ok {
			add("screen %s is missing", name)
			continue
		}
		if fmt.Sprint(ws.Kitty) != fmt.Sprint(gs.Kitty) {
			add("%s kitty keyboard flags: want %v, got %v", name, ws.Kitty, gs.Kitty)
		}
		if fmt.Sprint(ws.SavedCursor) != fmt.Sprint(gs.SavedCursor) {
			add("%s saved cursor: want %v, got %v", name, ws.SavedCursor, gs.SavedCursor)
		}
		diffs = append(diffs, vtCompareRows(name+" scrollback", ws.Scrollback, gs.Scrollback, true)...)
		diffs = append(diffs, vtCompareRows(name+" screen", ws.Active, gs.Active, false)...)
	}
	for name := range got.Screens {
		if _, ok := want.Screens[name]; !ok {
			add("unexpected screen %s", name)
		}
	}
	if len(diffs) > 0 {
		if len(diffs) > 30 {
			diffs = append(diffs[:30], fmt.Sprintf("… and %d more", len(diffs)-30))
		}
		t.Errorf("the restored terminal differs:\n  %s", strings.Join(diffs, "\n  "))
	}
}

func vtCompareRows(what string, want, got []vtDumpRow, scrollback bool) []string {
	var diffs []string
	if len(want) != len(got) {
		diffs = append(diffs, fmt.Sprintf("%s: want %d rows, got %d", what, len(want), len(got)))
	}
	for i := range min(len(want), len(got)) {
		w, g := want[i], got[i]
		// The last scrollback row may wrap onto the screen in the original.
		// A snapshot ends the scrollback with a line break.
		if scrollback && i == len(want)-1 {
			g.Wrap = w.Wrap
		}
		switch {
		case w.Text != g.Text:
			diffs = append(diffs, fmt.Sprintf("%s row %d text:\n      want %q\n      got  %q", what, i, w.Text, g.Text))
		case !bytes.Equal(w.Styles, g.Styles):
			diffs = append(diffs, fmt.Sprintf("%s row %d styles:\n      want %s\n      got  %s", what, i, w.Styles, g.Styles))
		case w.Wrap != g.Wrap:
			diffs = append(diffs, fmt.Sprintf("%s row %d wrap: want %v, got %v (%q)", what, i, w.Wrap, g.Wrap, w.Text))
		}
	}
	return diffs
}

// vtCheckClean fails if the snapshot holds RIS or anything that asks the
// terminal for a reply.
func vtCheckClean(t *testing.T, snap []byte) {
	t.Helper()
	var p term.Parser
	p.Feed(snap, func(tok term.Token) {
		if tok.Kind == term.Esc && bytes.Equal(tok.Raw, []byte("\x1bc")) {
			t.Errorf("snapshot contains RIS")
		}
		if term.IsQuery(tok) {
			t.Errorf("snapshot contains a query: %q", tok.Raw)
		}
	})
	if rest := p.Flush(); len(rest) > 0 {
		t.Errorf("snapshot ends inside a sequence: %q", rest)
	}
}

// vtRoundTrip feeds data to a shadow terminal, restores its snapshot into a
// fresh one, and compares the two.
func vtRoundTrip(t *testing.T, data []byte, cols, rows, snapCols, snapRows, maxLines int) []byte {
	t.Helper()
	src := vtStartTest(t, VTOptions{Cols: cols, Rows: rows})
	vtFeedAll(src, data)
	snap, upTo, err := src.Snapshot(snapCols, snapRows, maxLines, 10*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if upTo != int64(len(data)) {
		t.Errorf("snapshot reaches offset %d, want %d", upTo, len(data))
	}
	vtCheckClean(t, snap)
	want := vtDumpOf(t, src, maxLines)

	dst := vtStartTest(t, VTOptions{Cols: snapCols, Rows: snapRows})
	vtFeedAll(dst, snap)
	got := vtDumpOf(t, dst, maxLines)
	vtCompareDumps(t, want, got)
	return snap
}

type vtFixture struct {
	Name       string
	Cols, Rows int
}

func vtLoadFixtures(t *testing.T) []vtFixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "vt", "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx []vtFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

func vtReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "vt", name+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVTSnapshotFixtures(t *testing.T) {
	vtBinary(t)
	for _, fx := range vtLoadFixtures(t) {
		t.Run(fx.Name, func(t *testing.T) {
			data := vtReadFixture(t, fx.Name)
			vtRoundTrip(t, data, fx.Cols, fx.Rows, fx.Cols, fx.Rows, 1000)
			// A client of another size: the snapshot is laid out for it.
			t.Run("resized", func(t *testing.T) {
				vtRoundTrip(t, data, fx.Cols, fx.Rows, fx.Cols-17, fx.Rows+5, 1000)
			})
			// Every prefix point: the program is caught mid-frame.
			t.Run("cut", func(t *testing.T) {
				for _, frac := range []int{3, 2} {
					vtRoundTrip(t, data[:len(data)/frac], fx.Cols, fx.Rows, fx.Cols, fx.Rows, 1000)
				}
			})
		})
	}
}

// TestVTSnapshotModes checks that the modes a new client needs come back,
// with the kitty keyboard flags pushed per screen.
func TestVTSnapshotModes(t *testing.T) {
	data := vtReadFixture(t, "fullscreen")
	snap := vtRoundTrip(t, data, 100, 30, 100, 30, 100)
	for _, want := range []string{
		"\x1b[>1u\x1b[?1049h", // main screen flags, then the alternate screen
		"\x1b[>11u\x1b[>3u",   // alternate screen flags, as a stack
		"\x1b[?1002h", "\x1b[?1006h", "\x1b[?1004h", "\x1b[?2004h", "\x1b[?1h", "\x1b[?66h",
		"\x1b[6 q",
	} {
		if !bytes.Contains(snap, []byte(want)) {
			t.Errorf("snapshot lacks %q", want)
		}
	}
	if bytes.Contains(snap, []byte("\x1b[?2026h")) {
		t.Errorf("snapshot turns on synchronised output")
	}
}

// TestVTSnapshotCases checks state that a snapshot easily gets wrong.
func TestVTSnapshotCases(t *testing.T) {
	vtBinary(t)
	cases := []struct{ name, data string }{
		{"pending wrap", "\x1b[24;1H" + strings.Repeat("w", 80)},
		{"pending wrap on a wide character", "\x1b[24;1H" + strings.Repeat("w", 78) + "界"},
		{"pending wrap on a blank cell", "\x1b[3;80H \x1b[3;80H\x1b[K\x1b[3;79Hz"},
		{"wide character pushed to the next row", "\x1b[2;1H" + strings.Repeat("n", 79) + "界abc"},
		{"origin mode in a scroll region", "top\x1b[5;20r\x1b[?6h\x1b[3;4Hinside"},
		{"left and right margins", "\x1b[?69h\x1b[10;40s\x1b[5;5Hmargins"},
		{"autowrap off", "\x1b[?7l" + strings.Repeat("long ", 30) + "\r\nnext"},
		{"insert and newline modes", "abc\x1b[4h\x1b[20h\x1b[1GX"},
		{"reverse video and hidden cursor", "\x1b[?5h\x1b[?25ltext"},
		{"hyperlinks", "see \x1b]8;;https://example.com/a\x1b\\this link\x1b]8;;\x1b\\ and " +
			"\x1b]8;id=x1;https://example.com/b\x1b\\that\x1b]8;;\x1b\\.\r\nopen \x1b]8;;https://example.com/c\x1b\\still"},
		{"alternate screen 1047", "main text\x1b[?1047halt text\x1b[5;5Hhere"},
		{"alternate screen 47", "main text\x1b[?47halt text"},
		{"alternate screen then back", "one\x1b[?1049hfull\x1b[?1049ltwo"},
		{"saved cursor and pen", "\x1b[7;9H\x1b[1;35m\x1b7\x1b[0m\x1b[1;1Hhome"},
		{"graphemes", "\x1b[?2027hfamily 👨‍👩‍👧 flag 🇳🇿 e\u0301 ok\r\n"},
		{"tab after a background colour", "\x1b[44mA\tB\x1b[0m\tC\r\n\x1b[4mu\tv\x1b[0m"},
		{"erase with a background colour", "abcdefghij\x1b[1;3H\x1b[41m\x1b[4X\x1b[0m\r\n\x1b[42m\x1b[2K\x1b[0m\r\nafter"},
		{"line drawing charset left on", "\x1b(0lqqk\r\nx  x"},
		{"mouse modes in order", "\x1b[?1003h\x1b[?1000h\x1b[?1015h\x1b[?1006h"},
		{"kitty flags set, not pushed", "\x1b[=5;1u\x1b[>4;2m"},
		{"cursor shape", "\x1b[3 q"},
		{"colours", "\x1b]4;3;rgb:01/02/03\x1b\\\x1b]10;rgb:aa/bb/cc\x1b\\\x1b]12;rgb:11/22/33\x07\x1b]11;rgb:00/00/00\x1b\\\x1b]111\x1b\\"},
		{"title and pwd", "\x1b]0;título ✓\x07\x1b]7;file://host/tmp/a%20b\x1b\\"},
		{"styles", "\x1b[1;2;3;4;5;7;8;9;53mall\x1b[0m \x1b[4:3;58:2::1:2:3mcurly\x1b[0m \x1b[38;5;200;48;2;1;2;3mc\x1b[0m"},
		{"a trailing partial sequence", "text\x1b[38;2;1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vtRoundTrip(t, []byte(tc.data), 80, 24, 80, 24, 100)
		})
	}
}

// vtLogStream makes a long, colourful log with wrapped lines, after
// setting modes that only its start holds.
func vtLogStream(size int) []byte {
	var b bytes.Buffer
	b.WriteString("\x1b[>1u\x1b[?2004h\x1b]2;long job\x1b\\")
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;200m%06d\x1b[0m \x1b[1mINFO\x1b[0m request done in %dms ", i%256, (i*7)%256, i, i%997)
		if i%5 == 0 {
			b.WriteString(strings.Repeat("payload-", 25))
		}
		b.WriteString("\r\n")
	}
	b.WriteString("$ ")
	return b.Bytes()
}

// TestVTRingWrap is the case the shadow terminal exists for: the output
// is longer than the ring, so the ring alone has lost how it started.
func TestVTRingWrap(t *testing.T) {
	vtBinary(t)
	data := vtLogStream(RingSize + RingSize/4)
	snap := vtRoundTrip(t, data, 120, 40, 120, 40, 2000)
	if !bytes.Contains(snap, []byte("\x1b[>1u")) || !bytes.Contains(snap, []byte("\x1b[?2004h")) {
		t.Errorf("snapshot lost the modes set before the ring's start")
	}

	// What a replay of the ring gives instead: the modes are gone.
	r := ring.New(RingSize)
	r.Write(data)
	tail, _ := r.ReadFrom(r.Start())
	v := vtStartTest(t, VTOptions{Cols: 120, Rows: 40})
	vtFeedAll(v, tail)
	if d := vtDumpOf(t, v, 0); len(d.Screens["primary"].Kitty) > 1 || strings.Contains(d.Modes, "2004") {
		t.Errorf("a replay of the ring alone should not know the start's modes: %+v %q", d.Screens["primary"].Kitty, d.Modes)
	}
}

func TestVTQueries(t *testing.T) {
	var rec vtRecorder
	v := vtStartTest(t, VTOptions{OnWrite: rec.onWrite, OnEvent: rec.onEvent})

	// Off by default: the client's terminal answers.
	v.Feed(0, []byte("\x1b[c"))
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	if w, _ := rec.get(); len(w) > 0 {
		t.Fatalf("answered while answering is off: %q", w)
	}

	v.SetProfile(VTProfile{
		DA1: []int{62, 22, 52}, XTVersion: "ghostty 1.3.1",
		Foreground: "#cdd6f4", Background: "#1e1e2e", ColorScheme: "dark",
	})
	v.AnswerQueries(true)
	v.Resize(80, 24, 800, 480)
	q := "\x1b[c\x1b[>c\x1b[>q\x1b[?u\x1b[>5u\x1b[?u\x1b]10;?\x1b\\\x1b]11;?\x07\x1b[6n\x1b[14t\x1b[18t\x1b[?996n\x1b]7501;?\x1b\\"
	v.Feed(3, []byte(q))
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	w, _ := rec.get()
	for _, want := range []string{
		"\x1b[?62;22;52c",            // DA1 from the profile
		"\x1b[>1;10;0c",              // DA2, Ghostty's
		"\x1bP>|ghostty 1.3.1\x1b\\", // XTVERSION
		"\x1b[?0u", "\x1b[?5u",       // kitty flags, before and after a push
		"\x1b]10;rgb:cdcd/d6d6/f4f4\x1b\\", "\x1b]11;rgb:1e1e/1e1e/2e2e\x07",
		"\x1b[1;1R",       // the cursor position
		"\x1b[4;480;800t", // the text area in pixels
		"\x1b[8;24;80t",   // the text area in cells
		"\x1b[?997;1n",    // dark
	} {
		if !bytes.Contains(w, []byte(want)) {
			t.Errorf("no reply %q in %q", want, w)
		}
	}
	if bytes.Contains(w, []byte("7501")) {
		t.Errorf("answered the OSC 7501 query, which the holder answers: %q", w)
	}

	// Off again: no more replies.
	v.AnswerQueries(false)
	before := len(w)
	v.Feed(int64(3+len(q)), []byte("\x1b[c"))
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	if w, _ := rec.get(); len(w) != before {
		t.Errorf("answered after answering was turned off: %q", w[before:])
	}
}

func TestVTEvents(t *testing.T) {
	var rec vtRecorder
	v := vtStartTest(t, VTOptions{OnEvent: rec.onEvent})
	vtFeedAll(v, []byte("hi\a\x1b]9;build done\x1b\\\x1b]777;notify;Agent;needs you\x07\x1b]2;my title\x07"+
		"\x1b]9;4;1;42\x07\x1b]7501;state=blocked:kind=permission\x1b\\\x1b]7;file://h/tmp\x1b\\"))
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	_, ev := rec.get()
	got := fmt.Sprintf("%+v", ev)
	for _, want := range []string{
		"{Type:bell ",
		"{Type:notify Title: Body:build done ",
		"{Type:notify Title:Agent Body:needs you ",
		"{Type:title Title:my title ",
		"{Type:progress Title: Body: State:set Value:42 ",
		"{Type:status Title: Body: State:blocked Value:0 Data:state=blocked:kind=permission}",
		"{Type:pwd Title: Body: State: Value:0 Data:file://h/tmp}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no event %s in %s", want, got)
		}
	}
}

// TestVTRestart kills the child and checks that the VT rebuilds it from the
// history, without raising old events again, and keeps feeding it.
func TestVTRestart(t *testing.T) {
	var rec vtRecorder
	var mu sync.Mutex
	r := ring.New(RingSize)
	feed := func(v *VT, p []byte) {
		mu.Lock()
		defer mu.Unlock()
		off := r.End()
		r.Write(p)
		v.Feed(off, p)
	}
	v := vtStartTest(t, VTOptions{
		Cols: 60, Rows: 10,
		OnEvent: rec.onEvent,
		History: func() ([]byte, int64) {
			mu.Lock()
			defer mu.Unlock()
			data, _ := r.ReadFrom(r.Start())
			return data, r.Start()
		},
	})
	data := vtReadFixture(t, "shell")
	feed(v, data[:len(data)/2])
	feed(v, []byte("\a"))
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	_, ev := rec.get()
	bells := 0
	for _, e := range ev {
		if e.Type == "bell" {
			bells++
		}
	}

	v.mu.Lock()
	pid := v.child.cmd.Process.Pid
	v.mu.Unlock()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	// Output that arrives while the child is down.
	feed(v, data[len(data)/2:])

	deadline := time.Now().Add(10 * time.Second)
	for {
		v.mu.Lock()
		up := v.child != nil && v.child.cmd.Process.Pid != pid
		v.mu.Unlock()
		if up {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the shadow terminal was not restarted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}

	// The restarted terminal matches one fed everything once.
	ref := vtStartTest(t, VTOptions{Cols: 60, Rows: 10})
	all, _ := r.ReadFrom(0)
	vtFeedAll(ref, all)
	vtCompareDumps(t, vtDumpOf(t, ref, 500), vtDumpOf(t, v, 500))

	mu.Lock()
	end := r.End()
	mu.Unlock()
	if _, upTo, err := v.Snapshot(60, 10, 500, 5*time.Second); err != nil || upTo != end {
		t.Errorf("snapshot after the restart: offset %d, want %d (%v)", upTo, end, err)
	}

	_, ev = rec.get()
	n := 0
	for _, e := range ev {
		if e.Type == "bell" {
			n++
		}
	}
	if n != bells {
		t.Errorf("the replay raised old bells again: %d before, %d after", bells, n)
	}
}

// TestVTSnapshotTimeout checks that a stuck child cannot hold a caller.
func TestVTSnapshotTimeout(t *testing.T) {
	v := vtStartTest(t, VTOptions{})
	v.mu.Lock()
	pid := v.child.cmd.Process.Pid
	v.mu.Unlock()
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _, err := v.Snapshot(80, 24, 0, 200*time.Millisecond)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("want a timeout, got %v after %v", err, time.Since(start))
	}
	_ = syscall.Kill(pid, syscall.SIGCONT)
	// The late reply goes to the caller that gave up, not to the next one.
	if _, _, err := v.Snapshot(80, 24, 0, 5*time.Second); err != nil {
		t.Fatalf("snapshot after the stall: %v", err)
	}
}

// TestVTFallsBehind checks that a child that cannot keep up is dropped and
// rebuilt, and that Feed never waits for it.
func TestVTFallsBehind(t *testing.T) {
	var mu sync.Mutex
	r := ring.New(1 << 20)
	v := vtStartTest(t, VTOptions{History: func() ([]byte, int64) {
		mu.Lock()
		defer mu.Unlock()
		data, _ := r.ReadFrom(r.Start())
		return data, r.Start()
	}})
	v.mu.Lock()
	v.maxBehind = 64 << 10
	v.child.limit = v.maxBehind
	pid := v.child.cmd.Process.Pid
	v.mu.Unlock()
	_ = syscall.Kill(pid, syscall.SIGSTOP)

	start := time.Now()
	chunk := bytes.Repeat([]byte("x"), 1000)
	for range 1000 {
		mu.Lock()
		off := r.End()
		r.Write(chunk)
		v.Feed(off, chunk)
		mu.Unlock()
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Feed blocked for %v", d)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		v.mu.Lock()
		up := v.child != nil && v.child.cmd.Process.Pid != pid
		v.mu.Unlock()
		if up {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stalled shadow terminal was not replaced")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
}

func TestVTMissingBinary(t *testing.T) {
	_, err := StartVT(VTOptions{Path: filepath.Join(t.TempDir(), "missing")})
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("want an error with a hint, got %v", err)
	}
}

// The shadow terminal caps a size itself, by whatever path the size comes:
// the command line, a resize and a snapshot. The holder caps sizes first,
// so this test speaks to the process directly.
func TestVTCapsItsSize(t *testing.T) {
	cmd := exec.Command(vtBinary(t), "--cols", "65535", "--rows", "70000")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Wait() })
	r := bufio.NewReader(out)

	// size sends the frames, then asks for a dump, and returns its size.
	size := func(frames ...[]byte) (cols, rows int) {
		t.Helper()
		for _, f := range append(frames, vtFrame(vtFrameDump, []byte{0, 0, 0, 0})) {
			if _, err := in.Write(f); err != nil {
				t.Fatal(err)
			}
		}
		for {
			var hdr [5]byte
			if _, err := io.ReadFull(r, hdr[:]); err != nil {
				t.Fatal(err)
			}
			p := make([]byte, binary.LittleEndian.Uint32(hdr[1:]))
			if _, err := io.ReadFull(r, p); err != nil {
				t.Fatal(err)
			}
			if hdr[0] != vtFrameDump {
				continue // the reply to a snapshot
			}
			var d vtDump
			if err := json.Unmarshal(p, &d); err != nil {
				t.Fatalf("dump is not JSON: %v", err)
			}
			return d.Cols, d.Rows
		}
	}
	sizeFrame := func(kind byte, cols, rows uint16, extra ...byte) []byte {
		p := binary.LittleEndian.AppendUint16(nil, cols)
		p = binary.LittleEndian.AppendUint16(p, rows)
		return vtFrame(kind, append(p, extra...))
	}

	if cols, rows := size(); cols != 4096 || rows != 2048 {
		t.Errorf("--cols 65535 --rows 70000 started it at %d by %d, want 4096 by 2048", cols, rows)
	}
	if cols, rows := size(sizeFrame(vtFrameResize, 100, 30), sizeFrame(vtFrameResize, 65535, 65535)); cols != 4096 || rows != 2048 {
		t.Errorf("a resize to 65535 by 65535 set %d by %d, want 4096 by 2048", cols, rows)
	}
	if cols, rows := size(sizeFrame(vtFrameResize, 100, 30), sizeFrame(vtFrameSnapshot, 65535, 65535, 0, 0, 0, 0)); cols != 4096 || rows != 2048 {
		t.Errorf("a snapshot at 65535 by 65535 set %d by %d, want 4096 by 2048", cols, rows)
	}
	if cols, rows := size(sizeFrame(vtFrameResize, 4096, 2048)); cols != 4096 || rows != 2048 {
		t.Errorf("a resize to the caps set %d by %d, want 4096 by 2048", cols, rows)
	}
	if cols, rows := size(sizeFrame(vtFrameResize, 4096, 30)); cols != 4096 || rows != 30 {
		t.Errorf("a resize within the caps set %d by %d, want 4096 by 30", cols, rows)
	}
}
