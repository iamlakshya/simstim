package egress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSentinelInvalidShapeAndValueNeverReadEntropyOrGuessAFormat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		real  api.Secret
		shape sentinelShape
	}{
		{"prefix mismatch", "qq_A", sentinelShape{Prefix: "pk_", Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}},
		{"byte length mismatch", "pk_AB", sentinelShape{Prefix: "pk_", Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}},
		{"outside alphabet", "D", sentinelShape{Slots: []sentinelSlot{{Alphabet: "ABC", Count: 1}}}},
		{"invalid real UTF8", api.Secret(string([]byte{0xff})), sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}},
		{"invalid prefix UTF8", "A", sentinelShape{Prefix: string([]byte{0xff}), Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}},
		{"mixed UTF8 widths", "A", sentinelShape{Slots: []sentinelSlot{{Alphabet: "Aβ", Count: 1}}}},
		{"duplicate alphabet", "A", sentinelShape{Slots: []sentinelSlot{{Alphabet: "ABA", Count: 1}}}},
		{"empty alphabet", "A", sentinelShape{Slots: []sentinelSlot{{Alphabet: "", Count: 1}}}},
		{"empty run", "", sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: 0}}}},
		{"negative run", "", sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: -1}}}},
		{"representation overflow", "A", sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: proto.MaxFrame + 1}}}},
		{"NUL alphabet", "A", sentinelShape{Slots: []sentinelSlot{{Alphabet: "A\x00", Count: 1}}}},
		{"control alphabet", "A", sentinelShape{Slots: []sentinelSlot{{Alphabet: "A\n", Count: 1}}}},
		{"hidden format alphabet", "A", sentinelShape{Slots: []sentinelSlot{{Alphabet: "A\u200b", Count: 1}}}},
		{"fixed prefix only", "fixed", sentinelShape{Prefix: "fixed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, lookups := 0, 0
			value, err := makeSentinelWithRead(context.Background(), "TOKEN", &tc.real, tc.shape, func(api.Secret) bool { lookups++; return true }, func(context.Context, []byte) error { reads++; return nil })
			var public *proto.Error
			if value != "" || err == nil || reads != 0 || lookups != 0 || !errors.As(err, &public) || public.Hint == "" {
				t.Fatal("unsupported shape/value was guessed, sampled or lacked a typed refusal")
			}
		})
	}
}

func TestSentinelMissingAvailabilityAndContextFailClosed(t *testing.T) {
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}
	if value, err := makeSentinelWithRead(context.Background(), "TOKEN", nil, shape, nil, scriptedSentinelRead(1)); value != "" || err == nil {
		t.Fatal("missing registry uniqueness check was treated as empty registry")
	}
	if value, err := makeSentinelWithRead(nil, "TOKEN", nil, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(1)); value != "" || err == nil {
		t.Fatal("missing context was accepted")
	}
	if value, err := makeSentinelWithRead(context.Background(), "invalid name", nil, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(1)); value != "" || err == nil {
		t.Fatal("untrusted diagnostic name was accepted")
	}
}

func TestSentinelEntropyReadsAreBatchedAcrossLongCompactRuns(t *testing.T) {
	const size = 10000
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: size}}}
	reads := 0
	value, err := makeSentinelWithRead(context.Background(), "TOKEN", nil, shape, func(api.Secret) bool { return true }, func(_ context.Context, p []byte) error {
		reads++
		if len(p) != sentinelEntropyBatch {
			t.Error("randomness was requested per character rather than by batch")
		}
		clear(p)
		return nil
	})
	wantReads := (size + sentinelEntropyBatch - 1) / sentinelEntropyBatch
	if err != nil || len(value) != size || reads != wantReads || string(value) != strings.Repeat("A", size) {
		t.Fatal("compact run generation lost shape or bounded batch behaviour")
	}
}

func TestSentinelShapeAndResultsUseRedactedFormatting(t *testing.T) {
	marker := "private-descriptor-marker"
	shape := sentinelShape{Prefix: marker, Slots: []sentinelSlot{{Alphabet: marker, Count: 1}}}
	for _, object := range []any{shape, shape.Slots[0], api.Secret(marker)} {
		if strings.Contains(fmt.Sprintf("%v %+v %#v %s %q %x %X", object, object, object, object, object, object, object), marker) {
			t.Fatal("sentinel input/result formatting exposed its content")
		}
	}
}

func TestSentinelDefaultCryptoPreservesShapeAndCannotReplayRegisteredOutput(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	real := api.Secret("pk_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	shape := sentinelShape{Prefix: "pk_", Slots: []sentinelSlot{{Alphabet: alphabet, Count: 32}}}
	registered := map[api.Secret]bool{real: true}
	for range 2 {
		value, err := makeSentinel(context.Background(), "TOKEN", &real, shape, func(candidate api.Secret) bool { return !registered[candidate] })
		if err != nil || value == real || registered[value] || len(value) != len(real) || !strings.HasPrefix(string(value), "pk_") {
			t.Fatal("cryptographic generation reused a value or changed its shape")
		}
		if err := checkSentinelValue(context.Background(), "TOKEN", value, shape); err != nil {
			t.Fatal("cryptographic sentinel did not match the reviewed token alphabet")
		}
		registered[value] = true
	}
}
