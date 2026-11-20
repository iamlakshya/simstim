package client

import (
	"encoding/base64"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A path on the laptop that a hostile session wants the user to upload.
const victim = "/Users/me/My Docs/taxes 2026.pdf"

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// osc52 is an OSC 52 clipboard write of s, ended with BEL.
func osc52(s string) string { return "\x1b]52;c;" + b64(s) + "\a" }

// fakeClock is a clock that tests move by hand. The memory's goroutine
// reads it too.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) add(d time.Duration) { c.set(c.now().Add(d)) }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
}

// testReader is the readings of a guard, as the guard makes them, that keep
// what they record: the clipboard's writes in got, and the selections' in
// sel.
type testReader struct {
	*pasteGuard
	got, sel [][32]byte
}

func newTestReader() *testReader {
	r := &testReader{pasteGuard: &pasteGuard{}}
	r.readers = newReadings(r.record, nil, newFakeClock().now, nil)
	return r
}

func (r *testReader) record(_ int, sel bool, s ...[32]byte) error {
	if sel {
		r.sel = append(r.sel, s...)
	} else {
		r.got = append(r.got, s...)
	}
	return nil
}

func noRecord(bool, ...[32]byte) error { return nil }

// seen reports whether a paste of text would match a write that the reader
// recorded, or one that has not ended.
func (r *testReader) seen(text string) bool {
	s, ok := keySum(text)
	return ok && (slices.Contains(r.got, s) || slices.Contains(r.sel, s) || slices.Contains(r.inProgress(), s))
}

// readSplit feeds out to new readers, in two parts cut at many places and
// in parts of one byte, and checks whether each would refuse a paste of
// text: because it recorded the text, or because it lost track of a write.
func readSplit(t *testing.T, name, out, text string, want bool) {
	t.Helper()
	check := func(how string, parts ...string) {
		r := newTestReader()
		for _, p := range parts {
			r.observe([]byte(p))
		}
		if got := r.seen(text) || r.orphaned(); got != want {
			t.Errorf("%s, %s: refused = %v, want %v", name, how, got, want)
		}
	}
	for i := 0; i <= len(out); i += max(1, len(out)/61) {
		check("cut at "+strconv.Itoa(i), out[:i], out[i:])
	}
	var bytes []string
	for i := range len(out) {
		bytes = append(bytes, out[i:i+1])
	}
	check("byte by byte", bytes...)
}

// kittyWrite is one kitty 5522 transfer of the victim's path, its base64 cut into n packets
// at random places, so cuts fall inside the groups of four.
func kittyWrite(n int, seed uint64) string {
	data := b64(victim)
	r := rand.New(rand.NewPCG(seed, 1))
	cuts := []int{0}
	for range n - 1 {
		cuts = append(cuts, r.IntN(len(data)+1))
	}
	slices.Sort(cuts)
	cuts = append(cuts, len(data))
	out := "\x1b]5522;type=write\x1b\\"
	for i := range len(cuts) - 1 {
		out += "\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;" + data[cuts[i]:cuts[i+1]] + "\x1b\\"
	}
	return out + "\x1b]5522;type=wdata\x1b\\"
}

func TestClipReaderReadsWrites(t *testing.T) {
	data := b64(victim)
	quoted := strings.ReplaceAll(victim, " ", `\ `)
	urlPath := "/Users/me/a?>?>~~.pdf"
	if u := base64.URLEncoding.EncodeToString([]byte(urlPath)); !strings.ContainsAny(u, "-_") {
		t.Fatalf("the URL case needs base64 with - or _, not %s", u)
	}
	for _, c := range []struct{ name, out, text string }{
		{"OSC 52 ended by BEL", osc52(victim), victim},
		{"OSC 52 ended by ST", "\x1b]52;c;" + data + "\x1b\\", victim},
		{"OSC 52 ended by another sequence", "\x1b]52;c;" + data + "\x1b[0m", victim},
		{"OSC 52 ended by 0x9C", "\x1b]52;c;" + data + "\x9c", victim},
		{"OSC 52 aborted by CAN", "\x1b]52;c;" + data + "\x18", victim},
		{"OSC 52 with controls inside", "\x1b]52;c;" + data[:5] + "\r\n\x00" + data[5:] + "\a", victim},
		{"OSC 52 with no selection", "\x1b]52;;" + data + "\a", victim},
		{"OSC 52 with two selections", "\x1b]52;pc;" + data + "\a", victim},
		{"OSC 52 with only data", "\x1b]52;" + data + "\a", victim},
		{"OSC 52 with a zero before the number", "\x1b]052;c;" + data + "\a", victim},
		{"OSC 52 without padding", "\x1b]52;c;" + strings.TrimRight(data, "=") + "\a", victim},
		{"OSC 52 through tmux", "\x1bPtmux;\x1b\x1b]52;c;" + data + "\a\x1b\\", victim},
		{"OSC 52 after other output", "text \x1b[1mbold\x1b[0m\x1b]0;title\a" + osc52(victim) + "more", victim},
		// R1: the escape state keeps C0 controls, DEL and bytes from 0xA0.
		{"ESC CR ]", "\x1b\r]52;c;" + data + "\a", victim},
		{"ESC NUL ]", "\x1b\x00]52;c;" + data + "\a", victim},
		{"ESC BEL ]", "\x1b\a]52;c;" + data + "\a", victim},
		{"ESC DEL ]", "\x1b\x7f]52;c;" + data + "\a", victim},
		{"ESC SO ]", "\x1b\x0e]52;c;" + data + "\a", victim},
		{"ESC 0xA0 ]", "\x1b\xa0]52;c;" + data + "\a", victim},
		// R1 and C3: a raw 0x9D starts an OSC string outside ground.
		{"ESC 0x9D", "\x1b\x9d52;c;" + data + "\a", victim},
		{"CSI then 0x9D", "\x1b[\x9d52;c;" + data + "\a", victim},
		{"CSI parameters then 0x9D", "\x1b[38;2\x9d52;c;" + data + "\a", victim},
		{"DCS entry then 0x9D", "\x1bP\x9d52;c;" + data + "\x9c", victim},
		{"APC then 0x9D", "\x1b_Gx\x9d52;c;" + data + "\a", victim},
		{"0x9D then 0x9C", "\x1b[\x9d52;c;" + data + "\x9c", victim},
		// The text as a session may shape it.
		{"the path quoted", osc52("'" + victim + "'"), victim},
		{"the path escaped as a terminal drops it", osc52(quoted), victim},
		{"the path with controls in it", osc52("/Users/me/My Docs/\x1btaxes\x00 2026.pdf"), victim},
		{"the path with an invisible character", osc52("/Users/me/My Docs/tax\u200bes 2026.pdf"), victim},
		{"the path in upper case", osc52(strings.ToUpper(victim)), victim},
		{"the path with padding around it", osc52("  \n" + victim + "\t \r\n"), victim},
		{"the URL alphabet", "\x1b]52;c;" + base64.URLEncoding.EncodeToString([]byte(urlPath)) + "\a", urlPath},
		// C4: old kitty joins successive writes to a selection.
		{"kitty's append", osc52(victim[:11]) + osc52(victim[11:20]) + osc52(victim[20:]), victim},
		{"iTerm2's Copy", "\x1b]1337;Copy=:" + data + "\a", victim},
		{"iTerm2's Copy in lower case", "\x1b]1337;copy=:" + data + "\a", victim},
		// C4: one transfer, its base64 cut anywhere, over more than 16 packets.
		{"kitty's protocol in 3 packets", kittyWrite(3, 1), victim},
		{"kitty's protocol in 40 packets", kittyWrite(40, 2), victim},
		{"kitty's protocol with no end", strings.TrimSuffix(kittyWrite(5, 3), "\x1b]5522;type=wdata\x1b\\"), victim},
		// R7: the text between CopyToClipboard and EndCopy.
		{"iTerm2's CopyToClipboard", "\x1b]1337;CopyToClipboard=\a/Users/me/My \x1b[1mDocs/taxes 2026.pdf\x1b]1337;EndCopy\a", victim},
		{"iTerm2's CopyToClipboard with no end", "\x1b]1337;CopyToClipboard\a" + victim, victim},
		{"iTerm2's CopyToClipboard with a mistyped end", "\x1b]1337;CopyToClipboard=\a" + victim + "\x1b]1337;EndCopyX\a", victim},
		// V1: xterm with C1 controls on, raw or in UTF-8, in every state.
		{"raw C1 OSC in ground", "x\x9d52;c;" + data + "\x9c", victim},
		{"UTF-8 C1 OSC in ground", "x\u009d52;c;" + data + "\u009c", victim},
		{"raw C1 OSC after a CSI ended", "\x1b[1m\x9d52;c;" + data + "\a", victim},
		{"raw C1 OSC in a DCS string", "\x1bPq\x9d52;c;" + data + "\a", victim},
		{"UTF-8 C1 OSC in a DCS string", "\x1bPq\u009d52;c;" + data + "\a", victim},
		{"raw C1 OSC after a character", "\u00e9\x9d52;c;" + data + "\a", victim},
		{"UTF-8 ST inside an OSC string", "\x1b]52;c;" + data + "\u009cQUFB\a", victim},
		// V1, round 5: the readings part before any C1 control. In the escape
		// state Ghostty reads the bytes of a letter one by one, and its 0x9D
		// starts an OSC string. xterm reads the letter whole, and stays in
		// the escape state, so the "]" starts the OSC string.
		{"ESC, a letter with 0x9D, then ]", "\x1b\u00dd]52;c;" + data + "\a", victim},
		{"ESC, a letter with two 0x9D, then ]", "\x1b\u275d]52;c;" + data + "\a", victim},
	} {
		readSplit(t, c.name, c.out, c.text, true)
	}
	for _, c := range []struct{ name, out string }{
		{"no output", ""},
		{"plain text with the path", "cat " + victim + "\r\n"},
		{"a title with the path", "\x1b]0;" + victim + "\a\x1b]2;" + victim + "\x1b\\"},
		{"a link to the path", "\x1b]8;;file://" + victim + "\x1b\\x\x1b]8;;\x1b\\"},
		{"a clipboard read", "\x1b]52;c;?\a"},
		{"another write", osc52("/Users/me/other.pdf")},
		{"an iTerm2 image", "\x1b]1337;File=inline=1:" + data + "\a"},
		{"kitty reads, with data", "\x1b]5522;type=read;" + data + "\x1b\\"},
		{"a number that only starts like 52", "\x1b]521;c;" + data + "\a"},
		// Ghostty decodes UTF-8 in ground, and keeps 0x9D as data in DCS.
		{"0x9D inside a character in ground", "\u275e52;c;" + data + "\a"},
	} {
		readSplit(t, c.name, c.out, victim, false)
	}
}

// C1: kitty reads write nothing, so they cannot stand for a write.
func TestClipReaderKittyReadsAreNotWrites(t *testing.T) {
	r := newTestReader()
	r.observe([]byte(osc52(victim)))
	n := len(r.got)
	for range 16 {
		r.observe([]byte("\x1b]5522;type=read;Lg==\x1b\\"))
	}
	if len(r.got) != n {
		t.Fatalf("kitty reads recorded %d writes", len(r.got)-n)
	}
}

// An empty write records nothing, so it moves no entry's time.
func TestClipReaderEmptyWrites(t *testing.T) {
	r := newTestReader()
	r.observe([]byte(osc52("   \n ") + "\x1b]52;c;\a" + "\x1b]1337;CopyToClipboard\a\x1b]1337;EndCopy\a"))
	if len(r.got) != 0 {
		t.Fatalf("empty writes recorded %d hashes", len(r.got))
	}
}

// An unfinished copy does not count for ever.
func TestClipReaderCopyAges(t *testing.T) {
	clock := newFakeClock()
	r := newClipReader(noRecord, clock.now, nil)
	r.observe([]byte("\x1b]1337;CopyToClipboard\a" + victim))
	if len(r.inProgress()) != 1 {
		t.Fatal("a running copy is not checked")
	}
	clock.add(clipMaxAge + time.Second)
	if len(r.inProgress()) != 0 {
		t.Fatal("a copy that started more than a day ago is still checked")
	}
}

// The reader knows when the terminal is between sequences and characters.
func TestClipReaderIdle(t *testing.T) {
	for _, c := range []struct {
		out  string
		idle bool
	}{
		{"", true},
		{"text", true},
		{"\x1b[38;2;1", false},
		{"\x1b[1mtext", true},
		{"\x1b]0;title", false},
		{"\x1b]0;title\a", true},
		{"\x1b", false},
		{"é"[:1], false},
		{"é", true},
		{"\x1bP1;2", false},
		{osc52(victim), true},
	} {
		r := newTestReader()
		r.observe([]byte(c.out))
		if r.idle() != c.idle {
			t.Errorf("after %q: idle = %v, want %v", c.out, r.idle(), c.idle)
		}
	}
}

// The text hash decodes UTF-8 across parts as the whole text would.
func TestTextHashParts(t *testing.T) {
	texts := []string{victim, "日本語/パス.pdf", "a\xffb\xe2\x82", "\xe2\x82\xac€", "ſ/Kelvin K"}
	for _, s := range texts {
		want, _ := keySum(s)
		for i := 0; i <= len(s); i++ {
			for j := i; j <= len(s); j++ {
				h := newTextHash()
				h.write([]byte(s[:i]))
				h.write([]byte(s[i:j]))
				h.write([]byte(s[j:]))
				if got, _ := h.sum(); got != want {
					t.Fatalf("%q cut at %d and %d: the hash differs", s, i, j)
				}
			}
		}
	}
}

// R4: each write costs the same, however many came before: the reader
// streams its hash and keeps no text. 4000 writes take about four times
// as long as 1000.
func BenchmarkClipReaderWrites(b *testing.B) {
	for _, n := range []int{1000, 4000} {
		var out []byte
		for i := range n {
			out = append(out, osc52("/tmp/"+strconv.Itoa(i))...)
		}
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.SetBytes(int64(len(out)))
			for b.Loop() {
				r := newClipReader(noRecord, time.Now, nil)
				r.observe(out)
			}
		})
	}
}

// V1: kitty metadata as the pinned terminal reads it: the last of a
// repeated key wins, a field without "=" drops the packet, keys match
// exactly, and a value of any length is read.
func TestKittyMetadata(t *testing.T) {
	mime := "dGV4dC9wbGFpbg=="
	long := strings.Repeat("x", 10000)
	for _, c := range []struct {
		meta, op, mime string
		ok, bad        bool
	}{
		{"type=wdata:mime=" + mime, "wdata", "text/plain", true, false},
		{"type=read:type=wdata:mime=" + mime, "wdata", "text/plain", true, false},
		{"type=wdata:type=read", "read", "", true, false},
		{"type=wdata:mime=" + mime + ":junk", "", "", false, false},
		{"type=wdata::mime=" + mime, "", "", false, false},
		{" type=wdata", "", "", false, false},
		{"type=wdata:x=" + long + ":mime=" + mime, "wdata", "text/plain", true, false},
		{"type=wdata:mime=" + long, "wdata", "", true, true},
		{"type=wdata:mime=dGV4dC9wbGFpbg", "wdata", "", true, true},
		{"type=wdata:name=!!", "wdata", "", true, true},
		{"type=wdata:pw=" + strings.Repeat("QUFB", 100), "wdata", "", true, false},
		{"type=wdata:pw=!!!!", "wdata", "", true, true},
		{"type=walias", "walias", "", true, false},
		{"type=writ", "", "", false, false},
		{"", "", "", false, false},
	} {
		var r kittyMetaReader
		for i := range len(c.meta) {
			r.byte(c.meta[i])
		}
		m, ok, bad := r.meta()
		if ok != c.ok || bad != c.bad || ok && (m.op != c.op || m.mime != c.mime) {
			t.Errorf("%.60q: %+v ok=%v bad=%v", c.meta, m, ok, bad)
		}
	}
}

// V1: a packet whose type repeats, read then wdata, carries data.
func TestClipReaderKittyRepeatedType(t *testing.T) {
	out := "\x1b]5522;type=write\x1b\\\x1b]5522;type=read:type=wdata:mime=dGV4dC9wbGFpbg==;" + b64(victim) +
		"\x1b\\\x1b]5522;type=wdata\x1b\\"
	readSplit(t, "type=read:type=wdata", out, victim, true)
	out = "\x1b]5522;type=write\x1b\\\x1b]5522;type=wdata:type=read:mime=dGV4dC9wbGFpbg==;" + b64(victim) +
		"\x1b\\\x1b]5522;type=wdata\x1b\\"
	readSplit(t, "type=wdata:type=read", out, victim, false)
}

// V4: a kitty transfer that a detach cuts goes on in the next attach of
// the same process. In a new process, data with no transfer refuses drops.
func TestClipReaderKittyAcrossDetach(t *testing.T) {
	m, _ := testMemory(t)
	data := b64(victim)
	first := newPasteGuard(m)
	first.observe([]byte("\x1b]5522;type=write\x1b\\\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;" + data[:20] + "\x1b\\"))
	first.detach()
	second := newPasteGuard(m)
	second.observe([]byte("\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;" + data[20:] + "\x1b\\\x1b]5522;type=wdata\x1b\\"))
	m.flush(5 * time.Second)
	if why, match := m.check([][32]byte{sumOf(t, victim)}, nil); match != 0 {
		t.Fatalf("a transfer across a detach was missed: %q", why)
	}
	if second.orphaned() {
		t.Error("the next attach in the process took its data for an orphan")
	}
	fresh := newClipReader(m.add, m.now, nil)
	fresh.observe([]byte("\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;" + data[20:] + "\x1b\\"))
	if !fresh.orphaned() {
		t.Error("data with no transfer in a new process was not an orphan")
	}
}

// V6: a write to a selection records as one; the clipboard's do not.
func TestClipReaderSelections(t *testing.T) {
	for _, c := range []struct {
		out string
		sel bool
	}{
		{"\x1b]52;c;" + b64(victim) + "\a", false},
		{"\x1b]52;;" + b64(victim) + "\a", false},
		{"\x1b]52;x;" + b64(victim) + "\a", false},
		{"\x1b]52;0;" + b64(victim) + "\a", false},
		{"\x1b]52;p;" + b64(victim) + "\a", true},
		{"\x1b]52;pp;" + b64(victim) + "\a", true},
		{"\x1b]5522;type=write:loc=primary\x1b\\\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;" + b64(victim) + "\x1b\\\x1b]5522;type=wdata\x1b\\", true},
		{"\x1b]1337;Copy=:" + b64(victim) + "\a", false},
	} {
		r := newTestReader()
		r.observe([]byte(c.out))
		if c.sel && (len(r.got) != 0 || len(r.sel) == 0) || !c.sel && (len(r.got) == 0 || len(r.sel) != 0) {
			t.Errorf("%q: %d clipboard and %d selection hashes", c.out, len(r.got), len(r.sel))
		}
	}
}

// V7: an empty write adds no text, so it renews no running text, and the
// write before it ages out on time.
func TestClipReaderEmptyWritesRenewNothing(t *testing.T) {
	m, clock := testMemory(t)
	r := newClipReader(m.add, m.now, nil)
	r.observe([]byte(osc52(victim)))
	for range 3 {
		clock.add(9 * time.Hour)
		r.observe([]byte("\x1b]52;c;\a\x1b]52;c;" + b64("  \n") + "\a"))
	}
	m.flush(5 * time.Second)
	if why, _ := m.check([][32]byte{sumOf(t, victim)}, nil); why != "" {
		t.Fatalf("27 hours after the write, with empty writes since: %q", why)
	}
}

// W2 and V3: a selection of several letters counts for each of them, and
// "s" for both, as xterm may send it to either.
func TestClipReaderMixedSelections(t *testing.T) {
	for _, sel := range []string{"pc", "px", "sx0", "s", "ps"} {
		r := newTestReader()
		r.observe([]byte("\x1b]52;" + sel + ";" + b64(victim) + "\a"))
		if len(r.got) == 0 || len(r.sel) == 0 {
			t.Errorf("%q: %d clipboard and %d selection hashes, want both", sel, len(r.got), len(r.sel))
		}
	}
}

// W1: both readings of 0x9C. Inside a UTF-8 letter of a kitty field, the
// pinned terminal reads it as text and goes on to the data. In xterm it ends
// the string. The guard records what each reading yields.
func TestClipReaderBothReadingsOf9C(t *testing.T) {
	ghostty := "\x1b]5522;type=write\x1b\\\x1b]5522;type=wdata:loc=\u00dc:mime=dGV4dC9wbGFpbg==;" + b64(victim) +
		"\x1b\\\x1b]5522;type=wdata\x1b\\"
	readSplit(t, "a kitty field with \u00dc", ghostty, victim, true)
	xterm := "\x1b]52;c;" + b64(victim) + "\x9cQUFBQUFB\a"
	readSplit(t, "OSC 52 that 0x9C ends in xterm", xterm, victim, true)
}

// V2: an OSC 52 selection runs to its ";". One longer than 16 bytes makes
// the reader lose track of the write, for a day, and the data still counts.
func TestClipReaderLongSelection(t *testing.T) {
	r := newTestReader()
	r.observe([]byte("\x1b]52;" + strings.Repeat("c", 17) + ";" + b64(victim) + "\a"))
	if !r.orphaned() {
		t.Error("a selection of 17 letters did not make the reader unsure")
	}
	if !r.seen(victim) {
		t.Error("the data after a long selection was not recorded")
	}
	r = newTestReader()
	r.observe([]byte("\x1b]52;" + strings.Repeat("c", 16) + ";" + b64(victim) + "\a"))
	if r.orphaned() || !r.seen(victim) {
		t.Error("a selection of 16 letters was not read")
	}
}

// V6: a kitty transfer that the reader lost track of keeps the reader
// unsure until it ends, and for a day after. So does a commit for a
// transfer that it never saw start.
func TestClipReaderKittyUnsure(t *testing.T) {
	clock := newFakeClock()
	r := newClipReader(noRecord, clock.now, nil)
	r.observe([]byte("\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;QUFB\x1b\\"))
	clock.add(25 * time.Hour)
	if !r.orphaned() {
		t.Fatal("a transfer that the reader lost track of stopped counting while it runs")
	}
	r.observe([]byte("\x1b]5522;type=wdata\x1b\\"))
	clock.add(23 * time.Hour)
	if !r.orphaned() {
		t.Fatal("it stopped counting less than a day after its end")
	}
	clock.add(2 * time.Hour)
	if r.orphaned() {
		t.Fatal("it still counts more than a day after its end")
	}
	fresh := newClipReader(noRecord, clock.now, nil)
	fresh.observe([]byte("\x1b]5522;type=wdata\x1b\\"))
	if !fresh.orphaned() {
		t.Fatal("a commit of a transfer that the reader never saw start did not count")
	}
}

// V7: a transfer keeps at most 64 finished streams, then is unsure.
// Alternating types do not make it slow or large.
func TestClipReaderKittyManyStreams(t *testing.T) {
	r := newClipReader(noRecord, time.Now, nil)
	var out []byte
	out = append(out, "\x1b]5522;type=write\x1b\\"...)
	for i := range 100_000 {
		mime := "dGV4dC9wbGFpbg=="
		if i%2 == 1 {
			mime = "dGV4dC9odG1s"
		}
		out = append(out, "\x1b]5522;type=wdata:mime="+mime+";"+b64(strconv.Itoa(i))+"\x1b\\"...)
	}
	start := time.Now()
	r.observe(out)
	if d := time.Since(start); d > stressLimit {
		t.Errorf("100,000 streams took %v", d)
	}
	if k := r.term.kitty; len(k.done) > kittyDone || !k.unsure || !r.orphaned() {
		t.Errorf("the transfer keeps %d hashes, unsure %v", len(k.done), k.unsure)
	}
}

// W6: a transfer keeps the hash of each MIME type's text until its commit,
// and records them all again then, so a commit a day later still counts.
func TestClipReaderKittyLateCommit(t *testing.T) {
	m, clock := testMemory(t)
	r := newClipReader(m.add, m.now, nil)
	r.observe([]byte("\x1b]5522;type=write\x1b\\\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;" + b64(victim) +
		"\x1b\\\x1b]5522;type=wdata:mime=dGV4dC9odG1s;" + b64("<p>hi</p>") + "\x1b\\"))
	clock.add(25 * time.Hour)
	r.observe([]byte("\x1b]5522;type=wdata\x1b\\"))
	m.flush(5 * time.Second)
	if why, _ := m.check([][32]byte{sumOf(t, victim)}, nil); why == "" {
		t.Fatal("a stream was forgotten before the commit that put it on the clipboard")
	}
}
