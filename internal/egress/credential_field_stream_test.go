package egress

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCredentialFieldStreamMatchesUnchangedMaterialisedFormatter(t *testing.T) {
	for _, tc := range []struct{ kind, user, value string }{
		{"raw", "", ""}, {"raw", "", "abc"}, {"raw", "", " a"}, {"raw", "", "a "},
		{"raw", "", "\ta"}, {"raw", "", "a\t"}, {"raw", "", "a\tb"}, {"raw", "", "a\x7f"},
		{"raw", "", "é🙂"}, {"raw", "", "\xff"},
		{"bearer", "", ""}, {"bearer", "", "="}, {"bearer", "", "A="}, {"bearer", "", "A==="},
		{"bearer", "", "Ab09-._~+/"}, {"bearer", "", "a=Z"}, {"bearer", "", "a b"}, {"bearer", "", "é"},
		{"basic", "", ""}, {"basic", "user", "abc"}, {"basic", "é🙂", "é"},
		{"basic", "user", "a\x00\r\nb"}, {"unknown", "", "abc"},
	} {
		// This actual formatter remains unchanged while the new stream
		// predicate receives differential evidence. Do not replace it yet.
		formatted, want := legacyRequestCredentialValue(tc.kind, tc.user, api.Secret(tc.value))
		for split := 0; split <= len(tc.value); split++ {
			var state CredentialFieldValidator
			state.Reset(t.Context(), tc.kind, uint64(len(tc.user)))
			_, first := state.Write([]byte(tc.value[:split]))
			_, second := state.Write([]byte(tc.value[split:]))
			length, final := state.Finish()
			valid := first == nil && second == nil && final == nil
			if valid != want || valid && length != uint64(len(formatted)) {
				t.Fatal("stream field predicate/length differs from the current full formatter")
			}
		}
	}
}

func TestCredentialFieldStreamRejectsOverflowCancellationAndPartialPlans(t *testing.T) {
	var state CredentialFieldValidator
	state.Reset(t.Context(), "basic", ^uint64(0))
	if _, err := state.Finish(); err == nil {
		t.Fatal("overflowed Basic field length became representable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	state.Reset(ctx, "raw", 0)
	if _, err := state.Write([]byte("value")); !errors.Is(err, context.Canceled) {
		t.Fatal("field validator discarded its actual caller cancellation")
	}
	state.Reset(t.Context(), "bearer", 0)
	_, _ = state.Write([]byte("A="))
	if length, err := state.Finish(); err != nil || length != 9 {
		t.Fatal("valid token68 padding changed")
	}
	if _, err := state.Write([]byte("late")); err == nil {
		t.Fatal("finished field state accepted another value fragment")
	}
	state.Reset(t.Context(), "raw", 0)
	if length, err := state.Finish(); err != nil || length != 0 {
		t.Fatal("reset changed the existing empty raw formatter semantics")
	}
}
