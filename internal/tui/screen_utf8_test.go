package tui

import (
	"bytes"
	"context"
	"testing"
	"unicode/utf8"
)

func TestScreenClampKeepsUTF8Text(t *testing.T) {
	for _, text := range []string{"出力1000件", "Λ2026", "┛1024x", "力=1000", "Û12345", "力1000000000x"} {
		data := []byte(text)
		got, err := clampCSIBytes(context.Background(), data, 80, 24)
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("clamp %q = %q, %v", text, got, err)
		}
		lines, err := ScreenLinesContext(context.Background(), data, 80, 24)
		if err != nil || len(lines) != 24 || lines[0] != text {
			t.Errorf("screen %q = %q, %v", text, lines, err)
		}
	}
}

func TestScreenClampKeepsAllUTF8ContinuationCSIBytes(t *testing.T) {
	var encoded [utf8.UTFMax]byte
	for r := rune(0xa0); r <= utf8.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		b := utf8.AppendRune(encoded[:0], r)
		if bytes.IndexByte(b, 0x9b) < 0 {
			continue
		}
		data := append(append([]byte(nil), b...), []byte("1000000x")...)
		got, err := clampCSIBytes(context.Background(), data, 80, 24)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("U+%04X text changed: %q -> %q, %v", r, data, got, err)
		}
	}
}

func TestScreenClampLeavesOrdinaryIncompleteCSI(t *testing.T) {
	for _, data := range [][]byte{[]byte("A\x1b[1000"), []byte("A\x9b2026="), []byte("A\x1b[20;1000 ")} {
		got, err := clampCSIBytes(context.Background(), data, 80, 24)
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("incomplete %q -> %q, %v", data, got, err)
		}
	}
	for _, data := range [][]byte{[]byte("A\x1b[2000000000b"), []byte("A\x9b2000000000b"), []byte("A\x1b[99999999999999999999999999")} {
		got, err := clampCSIBytes(context.Background(), data, 80, 24)
		if err != nil || bytes.Equal(got, data) {
			t.Errorf("oversized CSI %q was not bounded: %q, %v", data, got, err)
		}
	}
}
