package keys

import (
	"bytes"
	"math/rand"
	"testing"
)

var (
	detach = Key{Code: '\\', Mods: ModCtrl}
	paste  = Key{Code: 'v', Mods: ModCtrl}
)

func feedAll(f *Filter, chunks ...[]byte) []Piece {
	var out []Piece
	for _, c := range chunks {
		out = append(out, f.Feed(c)...)
	}
	if b := f.Flush(); len(b) > 0 {
		out = append(out, Piece{Kind: Pass, Raw: b})
	}
	return out
}

func joined(ps []Piece) []byte {
	var b []byte
	for _, p := range ps {
		b = append(b, p.Raw...)
	}
	return b
}

func TestMatchesEveryEncoding(t *testing.T) {
	cases := []struct {
		in      string
		idx     int
		release bool
	}{
		{"\x1c", 0, false},
		{"\x1b[92;5u", 0, false},
		{"\x1b[92;5:1u", 0, false},
		{"\x1b[92;5:3u", 0, true},
		{"\x1b[92;69u", 0, false},  // caps lock
		{"\x1b[92;133u", 0, false}, // num lock
		{"\x1b[92:124;5u", 0, false},
		{"\x1b[27;5;92~", 0, false},
		{"\x16", 1, false},
		{"\x1b[118;5u", 1, false},
		{"\x1b[27;5;118~", 1, false},
	}
	for _, c := range cases {
		f := New(detach, paste)
		ps := feedAll(f, []byte("a"+c.in+"b"))
		if len(ps) != 3 || ps[1].Kind != Match || ps[1].Index != c.idx || ps[1].Release != c.release {
			t.Errorf("%q: got %+v", c.in, ps)
		}
		if !bytes.Equal(joined(ps), []byte("a"+c.in+"b")) {
			t.Errorf("%q: bytes changed", c.in)
		}
	}
}

func TestPassesOtherKeys(t *testing.T) {
	for _, in := range []string{"\x1b[118;6u", "\x1b[97;5u", "\x01", "\x1b[13;2u", "\x1b[A", "\x1bOA", "\x1b[?1u", "\x1bv", "\x1b[27;6;92~"} {
		f := New(detach, paste)
		ps := feedAll(f, []byte(in))
		for _, p := range ps {
			if p.Kind != Pass {
				t.Errorf("%q: unexpected %+v", in, p)
			}
		}
		if !bytes.Equal(joined(ps), []byte(in)) {
			t.Errorf("%q: bytes changed to %q", in, joined(ps))
		}
	}
}

func TestPasteIsCollectedWhole(t *testing.T) {
	f := New(detach, paste)
	ps := feedAll(f, []byte("x\x1b[200~/Users/me/a.png\x16"), []byte(" b\x1b[20"), []byte("1~y"))
	if len(ps) != 3 || ps[1].Kind != Paste || string(ps[1].Text) != "/Users/me/a.png\x16 b" {
		t.Fatalf("got %+v", ps)
	}
	if string(joined(ps)) != "x\x1b[200~/Users/me/a.png\x16 b\x1b[201~y" {
		t.Fatalf("bytes changed: %q", joined(ps))
	}
}

// A lone ESC at the end of a read is the Esc key: it goes out in the same
// call, and the flush timer has nothing left to send.
func TestLoneEscGoesAtOnce(t *testing.T) {
	for _, in := range []string{"\x1b", "a\x1b", "\x1b\x1b", "\x1b[A\x1b", "\x1b[1\x1b"} {
		f := New(detach, paste)
		ps := f.Feed([]byte(in))
		if string(joined(ps)) != in || len(f.buf) != 0 {
			t.Errorf("%q: got %+v, holds %q", in, ps, f.buf)
		}
		if b := f.Flush(); len(b) != 0 || f.Pending() {
			t.Errorf("%q: the flush sent %q, pending=%v", in, b, f.Pending())
		}
	}
}

// The start of a CSI is held. If nothing follows, the flush timer sends it
// unchanged. If the rest follows, the key is found whole.
func TestUnfinishedCSIIsHeld(t *testing.T) {
	f := New(detach, paste)
	ps := f.Feed([]byte("a\x1b[1"))
	if string(joined(ps)) != "a" || !f.Pending() {
		t.Fatalf("got %+v pending=%v", ps, f.Pending())
	}
	if b := f.Flush(); string(b) != "\x1b[1" || f.Pending() {
		t.Fatalf("the flush sent %q, pending=%v", b, f.Pending())
	}
	ps = feedAll(New(detach, paste), []byte("\x1b[92;"), []byte("5u"))
	if len(ps) != 1 || ps[0].Kind != Match || string(ps[0].Raw) != "\x1b[92;5u" {
		t.Fatalf("a key split inside its CSI: got %+v", ps)
	}
}

// The filter does not find a key that a read splits right after its ESC. That
// ESC went out at once, and to take the rest would leave the agent a stray Esc.
// Every byte goes on, unchanged and in order. Legacy Ctrl+\ and Ctrl+V have no
// ESC to split after.
func TestSplitAfterEscPassesThrough(t *testing.T) {
	altV := Key{Code: 'v', Mods: ModAlt} // the image paste key on Windows
	for _, in := range []string{"\x1b[92;5u", "\x1b[92;5:3u", "\x1b[118;5u", "\x1b[118;5:2u", "\x1b[27;5;92~", "\x1b[27;5;118~", "\x1bv"} {
		ps := feedAll(New(detach, paste, altV), []byte(in[:1]), []byte(in[1:]))
		for _, p := range ps {
			if p.Kind != Pass {
				t.Errorf("%q split after its ESC: unexpected %+v", in, p)
			}
		}
		if string(joined(ps)) != in {
			t.Errorf("%q split after its ESC: bytes changed to %q", in, joined(ps))
		}
		// In one read, the same bytes are the key.
		if ps := feedAll(New(detach, paste, altV), []byte(in)); len(ps) != 1 || ps[0].Kind != Match {
			t.Errorf("%q in one read: got %+v", in, ps)
		}
	}
}

// A paste's start marker is found wherever a read splits it, so the keys in
// the paste are never taken. Split right after its ESC, the paste passes
// through as it came, since that ESC already went out.
func TestSplitPasteStart(t *testing.T) {
	const in = "x\x1b[200~a\x1cb\x16c\x1b[201~y"
	cases := []struct {
		cuts  []int
		whole bool // a Paste piece, which can be rewritten
	}{
		{[]int{2}, false},
		{[]int{2, 3}, false},
		{[]int{2, 4, 6}, false},
		{[]int{2, 7}, false},
		{[]int{2, 13}, false},
		{[]int{4}, true},
		{[]int{3, 6}, true},
	}
	for _, c := range cases {
		var chunks [][]byte
		from := 0
		for _, at := range append(c.cuts, len(in)) {
			chunks = append(chunks, []byte(in[from:at]))
			from = at
		}
		ps := feedAll(New(detach, paste), chunks...)
		pastes := 0
		for _, p := range ps {
			if p.Kind == Match {
				t.Errorf("cuts %v: a key in the paste was taken: %+v", c.cuts, p)
			}
			if p.Kind == Paste {
				pastes++
			}
		}
		if (pastes == 1) != c.whole || string(joined(ps)) != in {
			t.Errorf("cuts %v: got %+v", c.cuts, ps)
		}
	}
	// After the flush timer, the ESC was the Esc key, and "[200~" is typed
	// text: the keys after it are found.
	f := New(detach, paste)
	ps := f.Feed([]byte("\x1b"))
	f.Flush()
	ps = append(ps, f.Feed([]byte("[200~\x1c"))...)
	if len(ps) != 3 || ps[2].Kind != Match || string(joined(ps)) != "\x1b[200~\x1c" {
		t.Errorf("after the flush: got %+v", ps)
	}
}

// Whatever the chunking, the filter must keep every byte, in order. It must
// find exactly the watched keys, except those that a read splits right after
// their ESC.
func TestSplitStreams(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	type piece struct {
		s     string
		match bool
	}
	vocab := []piece{
		{"hello", false}, {"\x1b[A", false}, {"\x1b[13;2u", false}, {"日本", false}, {"\r", false},
		{"\x1b[92;5u", true}, {"\x1c", true}, {"\x1b[118;5:1u", true}, {"\x1b[27;5;118~", true},
		{"\x1b[200~pasted \x1c text\x1b[201~", false},
	}
	for round := range 2000 {
		var stream []byte
		var escKeys []int // where each watched key that starts with ESC begins
		want := 0
		for range 20 {
			p := vocab[rng.Intn(len(vocab))]
			if p.match {
				want++
				if p.s[0] == 0x1b {
					escKeys = append(escKeys, len(stream))
				}
			}
			stream = append(stream, p.s...)
		}
		f := New(detach, paste)
		var chunks [][]byte
		cut := map[int]bool{} // where a read begins
		for i := 0; i < len(stream); {
			n := 1 + rng.Intn(12)
			if i+n > len(stream) {
				n = len(stream) - i
			}
			chunks = append(chunks, stream[i:i+n])
			i += n
			cut[i] = true
		}
		for _, at := range escKeys {
			if cut[at+1] {
				want--
			}
		}
		ps := feedAll(f, chunks...)
		got := 0
		for _, p := range ps {
			if p.Kind == Match {
				got++
			}
		}
		if !bytes.Equal(joined(ps), stream) {
			t.Fatalf("round %d: bytes changed\n got %q\nwant %q", round, joined(ps), stream)
		}
		if got != want {
			t.Fatalf("round %d: %d matches, want %d", round, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Key{"ctrl+\\": detach, "Ctrl+V": paste, "alt+v": {Code: 'v', Mods: ModAlt}} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := Parse("hyper+x"); err == nil {
		t.Error("expected an error for an unknown modifier")
	}
}

func FuzzFilter(f *testing.F) {
	for _, s := range []string{"a\x1c", "\x1b[92;5u", "\x1b[200~x\x1b[201~", "\x1b", "\x1b[", "\x1b[27;5;118~"} {
		f.Add([]byte(s), uint8(3))
	}
	f.Add([]byte("a\x1b[200~\x1c\x1b[201~\x1b[1"), uint8(1))
	f.Add([]byte("\x1b[200~x\x1b[201~\x1bv\x1b"), uint8(0x31))
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		// The low bits of step give the size of each read, and the high bits
		// after which reads the flush timer fires.
		n, flushes := int(step%16)+1, step>>4
		flt := New(detach, paste)
		var ps []Piece
		for i, k := 0, 0; i < len(data); i, k = i+n, k+1 {
			end := min(i+n, len(data))
			ps = append(ps, flt.Feed(data[i:end])...)
			checkFed(t, flt, ps, data[:end])
			if flushes&(1<<(k%4)) != 0 {
				ps = append(ps, Piece{Kind: Pass, Raw: flt.Flush()})
			}
		}
		ps = append(ps, Piece{Kind: Pass, Raw: flt.Flush()})
		// An unfinished paste is still held by the filter; that is fine.
		if flt.inPaste {
			return
		}
		if !bytes.Equal(joined(ps), data) {
			t.Fatalf("bytes changed: got %q want %q", joined(ps), data)
		}
	})
}

// checkFed checks the filter after each read: no byte is lost, doubled or
// moved, and it holds back only an unfinished CSI. A lone ESC never waits.
func checkFed(t *testing.T, flt *Filter, ps []Piece, fed []byte) {
	t.Helper()
	out := joined(ps)
	if flt.inPaste {
		if !bytes.HasPrefix(fed, out) {
			t.Fatalf("in a paste, sent %q of %q", out, fed)
		}
		return
	}
	if !bytes.Equal(append(out, flt.buf...), fed) {
		t.Fatalf("sent %q and held %q of %q", out, flt.buf, fed)
	}
	// A paste over MaxPaste leaves the tail it kept as a possible end marker.
	if held := flt.buf; len(held) > 0 && len(fed) <= MaxPaste {
		if _, complete, _ := csiLen(held); len(held) < 2 || held[0] != 0x1b || held[1] != '[' || complete {
			t.Fatalf("holds %q, which is not an unfinished CSI", held)
		}
	}
}
