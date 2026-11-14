package secretbind

import (
	"context"
	"errors"
	"testing"
)

func TestBindingValueStatesResetErrorsCompletionAndCanonicalModes(t *testing.T) {
	ctx := t.Context()
	var header HeaderValidator
	header.Reset(ctx)
	_, _ = header.Write([]byte("Host"))
	if header.Finish() == nil {
		t.Fatal("reserved header fixture was accepted")
	}
	header.Reset(ctx)
	if _, err := header.Write([]byte("x-api-key")); err != nil || header.Finish() != nil {
		t.Fatal("config reset retained a prior field error")
	}
	header.ResetCanonical(ctx)
	if _, err := header.Write([]byte("x-api-key")); err == nil {
		t.Fatal("canonical reset lost exact stored spelling")
	}
	header.Reset(ctx)
	if _, err := header.Write([]byte("x-api-key")); err != nil || header.Finish() != nil {
		t.Fatal("config reset retained the previous strict mode")
	}
	var format FormatValidator
	format.Reset(ctx)
	_, _ = format.Write([]byte("basic:unfinished"))
	if format.Finish() == nil {
		t.Fatal("incomplete format fixture was accepted")
	}
	format.Reset(ctx)
	if _, err := format.Write([]byte("Bearer {value}")); err != nil || format.Finish() != nil {
		t.Fatal("format reset retained a partial suffix or finished error")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	format.Reset(cancelled)
	if _, err := format.Write([]byte("{value}")); !errors.Is(err, context.Canceled) {
		t.Fatal("reset discarded its actual caller cancellation")
	}
	format.Reset(ctx)
	if _, err := format.Write([]byte("basic::{value}")); err != nil || format.Finish() != nil {
		t.Fatal("reset retained cancellation or refused the supported empty Basic user")
	}
}

func TestAuthorityValidationOnlyFinishPreservesOneGrammarAndReset(t *testing.T) {
	var state AuthorityValidator
	for _, tc := range []struct {
		raw, canonical string
		strict         bool
	}{
		{" MiXeD.example.com. ", "mixed.example.com:443", false},
		{"[2001:db8::1]:443", "[2001:db8::1]:443", true},
		{"192.0.2.1", "192.0.2.1:443", false},
		{"mixed.example.com:443", "mixed.example.com:443", true},
		{"mixed.example.com", "", true},
		{"MiXeD.example.com:443", "", true},
		{"https://example.com", "", false},
		{"example.com:8443", "", false},
		{"*.example.com", "", false},
		{"192.0.2.0/24", "", false},
	} {
		if tc.strict {
			state.ResetCanonical(t.Context())
		} else {
			state.Reset(t.Context())
		}
		var writeErr error
		for i := range len(tc.raw) {
			if _, err := state.Write([]byte(tc.raw[i : i+1])); err != nil {
				writeErr = err
				break
			}
		}
		err := state.FinishCheck()
		if tc.canonical == "" {
			if writeErr == nil && err == nil {
				t.Fatal("validation-only finish accepted an unsupported authority")
			}
			continue
		}
		if writeErr != nil || err != nil {
			t.Fatal("validation-only finish changed supported config or strict authority syntax")
		}
		canonical, err := state.Finish()
		if err != nil || canonical != tc.canonical {
			t.Fatal("materialising a checked authority changed its canonical identity")
		}
	}
}
