package term

import (
	"reflect"
	"strings"
	"testing"
)

type spanToken struct {
	kind                        Kind
	raw, params, inter, payload string
	prefix, final               byte
}

func keepSpanToken(out *[]spanToken) func(Token) {
	return func(tok Token) {
		n := spanToken{kind: tok.Kind, raw: string(tok.Raw), params: string(tok.Params),
			inter: string(tok.Intermediate), payload: string(tok.Payload), prefix: tok.Prefix, final: tok.Final}
		if n.kind == Text && len(*out) > 0 && (*out)[len(*out)-1].kind == Text {
			(*out)[len(*out)-1].raw += n.raw
			return
		}
		*out = append(*out, n)
	}
}

// Text includes raw controls, invalid UTF-8 and valid Unicode. Only ESC
// starts a sequence, wherever a transport splits the stream.
func TestParserTextSpansKeepSequencesAcrossChunks(t *testing.T) {
	before, middle, after := "\x00[\x9d\xff日本語", "\r\n\x7f\xc2", "\x80tail\a"
	stream := before + "\x1b[?2004h" + middle + "\x1b]2;title\x1b\\" + "\x1bP+q544e\x1b\\" + "\x1b(B" + after
	want := []spanToken{
		{kind: Text, raw: before},
		{kind: CSI, raw: "\x1b[?2004h", prefix: '?', params: "2004", final: 'h'},
		{kind: Text, raw: middle},
		{kind: OSC, raw: "\x1b]2;title\x1b\\", payload: "2;title"},
		{kind: DCS, raw: "\x1bP+q544e\x1b\\", payload: "+q544e"},
		{kind: Esc, raw: "\x1b(B", inter: "(", final: 'B'},
		{kind: Text, raw: after},
	}
	check := func(chunks ...string) {
		t.Helper()
		var p Parser
		var got []spanToken
		for _, chunk := range chunks {
			p.Feed([]byte(chunk), keepSpanToken(&got))
		}
		if p.Pending() || !reflect.DeepEqual(got, want) {
			t.Fatalf("chunks %q: pending %t, tokens %#v; want %#v", chunks, p.Pending(), got, want)
		}
	}
	for cut := range len(stream) + 1 {
		check(stream[:cut], stream[cut:])
	}
	for width := 1; width <= len(stream); width++ {
		var chunks []string
		for start := 0; start < len(stream); start += width {
			chunks = append(chunks, stream[start:min(start+width, len(stream))])
		}
		check(chunks...)
	}
}

func TestParserLongTextKeepsPendingEscape(t *testing.T) {
	text := strings.Repeat("日本語\x00\x80", 4096)
	var p Parser
	var got []spanToken
	p.Feed([]byte(text+"\x1b[?2004"), keepSpanToken(&got))
	if !p.Pending() || !reflect.DeepEqual(got, []spanToken{{kind: Text, raw: text}}) {
		t.Fatal("text before an incomplete sequence changed, or the sequence was not kept")
	}
	p.Feed([]byte("hafter"), keepSpanToken(&got))
	want := []spanToken{{kind: Text, raw: text},
		{kind: CSI, raw: "\x1b[?2004h", prefix: '?', params: "2004", final: 'h'},
		{kind: Text, raw: "after"}}
	if p.Pending() || !reflect.DeepEqual(got, want) {
		t.Fatal("completing a pending sequence after long text changed its bytes or parameters")
	}
}

func TestParserRunawayStringKeepsLaterSequences(t *testing.T) {
	text := "\x1b]0;" + strings.Repeat("x", maxSeq+1)
	var p Parser
	var got []spanToken
	p.Feed([]byte(text), keepSpanToken(&got))
	if p.Pending() || !reflect.DeepEqual(got, []spanToken{{kind: Text, raw: text}}) {
		t.Fatal("a sequence above the limit was not passed through as exact text")
	}
	p.Feed([]byte("\x1b[?2004h"), keepSpanToken(&got))
	want := []spanToken{{kind: Text, raw: text},
		{kind: CSI, raw: "\x1b[?2004h", prefix: '?', params: "2004", final: 'h'}}
	if p.Pending() || !reflect.DeepEqual(got, want) {
		t.Fatal("a sequence after a runaway string was lost or changed")
	}
}
