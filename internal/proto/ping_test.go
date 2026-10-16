package proto

import (
	"bytes"
	"testing"
)

// A timed ping works with old and new peers both ways: a new bridge adds its
// time, an old one echoes the ID alone, and other pings are echoed as before.
func TestTimedPing(t *testing.T) {
	ping := EncodePing(7)
	id, host, ok := DecodePong(Pong(ping, 123456789))
	if !ok || id != 7 || host != 123456789 {
		t.Fatalf("a new bridge's pong = %d, %d, %v", id, host, ok)
	}
	if id, host, ok := DecodePong(ping); !ok || id != 7 || host != 0 {
		t.Fatalf("an old bridge's echo = %d, %d, %v", id, host, ok)
	}
	for _, p := range [][]byte{nil, []byte("p"), []byte("an untimed ping")} {
		if got := Pong(p, 5); !bytes.Equal(got, p) {
			t.Errorf("the pong to %q is %q, want the same bytes", p, got)
		}
		if _, _, ok := DecodePong(p); ok {
			t.Errorf("%q decodes as a timed pong", p)
		}
	}
	// The pong is a new slice: the ping's buffer is not changed.
	if len(ping) != 8 {
		t.Errorf("the ping grew to %d bytes", len(ping))
	}
}

func TestTimes(t *testing.T) {
	if got := DecodeTime(EncodeTime(1 << 50)); got != 1<<50 {
		t.Fatalf("time = %d", got)
	}
	if got := DecodeTime([]byte{1, 2}); got != 0 {
		t.Fatalf("a short time = %d, want 0", got)
	}
}
