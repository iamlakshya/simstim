package secretbind

import "testing"

func TestFinishedFormatFactsKeepExactKindAndUsernameByteCount(t *testing.T) {
	var state FormatValidator
	for _, tc := range []struct {
		text, kind string
		bytes      uint64
	}{
		{"{value}", "raw", 0},
		{"Bearer {value}", "bearer", 0},
		{"basic::{value}", "basic", 0},
		{"basic:user:{value}", "basic", 4},
		{"basic:é🙂:{value}", "basic", 6},
	} {
		for split := 0; split <= len(tc.text); split++ {
			state.Reset(t.Context())
			if _, err := state.Write([]byte(tc.text[:split])); err != nil {
				t.Fatal(err)
			}
			if _, err := state.Write([]byte(tc.text[split:])); err != nil {
				t.Fatal(err)
			}
			kind, bytes, err := state.FinishFacts()
			if err != nil || kind != tc.kind || bytes != tc.bytes {
				t.Fatal("finished format facts changed kind or counted runes instead of UTF8 bytes")
			}
			if again, count, err := state.FinishFacts(); err != nil || again != kind || count != bytes {
				t.Fatal("format facts did not preserve completed-state idempotence")
			}
		}
	}
	state.Reset(t.Context())
	_, _ = state.Write([]byte("basic:old-user"))
	if kind, count, err := state.FinishFacts(); err == nil || kind != "" || count != 0 {
		t.Fatal("an invalid or incomplete format exposed usable metadata facts")
	}
	state.Reset(t.Context())
	_, _ = state.Write([]byte("basic::{value}"))
	if kind, count, err := state.FinishFacts(); err != nil || kind != "basic" || count != 0 {
		t.Fatal("reset retained a previous username byte count")
	}
}
