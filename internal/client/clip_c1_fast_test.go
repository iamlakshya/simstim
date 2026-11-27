package client

import (
	"bytes"
	"strings"
	"testing"
)

func TestC1UnicodeSpansKeepControlsAndChunkBoundaries(t *testing.T) {
	text := []byte("plain \x1b[31m日本語 ❝ ✓ →\x1b[0m")
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"complete text", text, text},
		{"non-C1 C2 character", []byte("a\u00a0b"), []byte("a\u00a0b")},
		{"replacement character", []byte("a\ufffdb"), []byte("a\ufffdb")},
		{"invalid lead before C1", []byte{0xe2, 0x9d, '5', '2'}, []byte{0xe2, 0x1b, ']', '5', '2'}},
		{"invalid non-C1 byte", []byte{'a', 0xff, 'b'}, []byte{'a', 0xff, 'b'}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertC1EverySplit(t, tc.in, tc.want)
		})
	}
	for c := byte(0x80); c <= 0x9f; c++ {
		want := append(append(bytes.Clone(text), 0x1b, c-0x40), text...)
		for _, control := range [][]byte{{c}, {0xc2, c}} {
			input := append(append(bytes.Clone(text), control...), text...)
			assertC1EverySplit(t, input, want)
		}
	}
}

func assertC1EverySplit(t *testing.T, input, want []byte) {
	t.Helper()
	for cut := range len(input) + 1 {
		var tr c1Translator
		got := bytes.Clone(tr.translate(input[:cut]))
		got = append(got, tr.translate(input[cut:])...)
		if !bytes.Equal(got, want) || len(tr.pend) != 0 {
			t.Fatalf("split %d of %x: got %x, pending %x; want %x", cut, input, got, tr.pend, want)
		}
		// The attach checks each part once, and the translator trusts it.
		var checked c1Translator
		a, b := input[:cut], input[cut:]
		got = bytes.Clone(checked.translateChecked(a, c1Free(a)))
		got = append(got, checked.translateChecked(b, c1Free(b))...)
		if !bytes.Equal(got, want) || len(checked.pend) != 0 {
			t.Fatalf("checked split %d of %x: got %x, pending %x; want %x", cut, input, got, checked.pend, want)
		}
	}
}

func TestPasteGuardUnicodeSpansStillRecordClipboardWrites(t *testing.T) {
	prefix := strings.Repeat("日本語 ❝ ✓ →\x1b[31mtext\x1b[0m ", 64)
	for _, write := range []string{
		osc52(victim),
		"\u009d52;c;" + b64(victim) + "\u009c",
		string([]byte{0xe2, 0x9d}) + "52;c;" + b64(victim) + string([]byte{0x9c}),
	} {
		for cut := range len(write) + 1 {
			r := newTestReader()
			r.observe(t.Context(), []byte(prefix+write[:cut]))
			r.observe(t.Context(), []byte(write[cut:]+prefix))
			if !r.seen(victim) {
				t.Fatalf("Unicode output hid a clipboard write at split %d: %q", cut, write)
			}
		}
	}
}
