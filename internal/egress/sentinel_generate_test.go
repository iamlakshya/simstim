package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func scriptedSentinelRead(values ...byte) sentinelRead {
	position := 0
	return func(_ context.Context, p []byte) error {
		for i := range p {
			p[i] = 0
			if position < len(values) {
				p[i] = values[position]
			}
			position++
		}
		return nil
	}
}

func TestSentinelSingleByteAvoidsRealRegistryAndPriorStartValues(t *testing.T) {
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "ABCD", Count: 1}}}
	real := api.Secret("A")
	// This is the caller's sole prospective registry view, not generator state.
	registered := map[api.Secret]bool{"A": true, "B": true}
	available := func(candidate api.Secret) bool { return !registered[candidate] }
	first, err := makeSentinelWithRead(context.Background(), "TOKEN", &real, shape, available, scriptedSentinelRead(0, 1, 2))
	if err != nil || first != "C" {
		t.Fatal("one-byte sentinel failed to avoid the real and current values")
	}
	registered[first] = true
	second, err := makeSentinelWithRead(context.Background(), "TOKEN", &real, shape, available, scriptedSentinelRead(2, 0, 1, 3))
	if err != nil || second != "D" || second == first {
		t.Fatal("a new generation replayed a persisted/current sentinel")
	}
}

func TestSentinelUnarmedGenerationNeedsOnlyPersistedSafeShape(t *testing.T) {
	shape := sentinelShape{Prefix: "pk_", Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}
	value, err := makeSentinelWithRead(context.Background(), "TOKEN", nil, shape, func(candidate api.Secret) bool {
		return candidate != "pk_A" // the previous persisted guest-visible sentinel
	}, scriptedSentinelRead(0, 1))
	if err != nil || value != "pk_B" {
		t.Fatal("unarmed generation required or replayed an old real value")
	}
}

func TestSentinelIndependentlyRefusesTheRealValue(t *testing.T) {
	real := api.Secret("A")
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}
	value, err := makeSentinelWithRead(context.Background(), "TOKEN", &real, shape, func(candidate api.Secret) bool {
		if candidate == real {
			t.Error("the real value reached registry availability as a candidate")
		}
		return true
	}, scriptedSentinelRead(0, 1))
	if err != nil || value != "B" {
		t.Fatal("real-value equality was delegated entirely to the caller")
	}
}

func TestSentinelPreservesReviewedUTF8WidthsAndKnownSeparators(t *testing.T) {
	shape := sentinelShape{Prefix: "κ_", Slots: []sentinelSlot{
		{Alphabet: "αβγ", Count: 2}, {Alphabet: ".", Count: 1}, {Alphabet: "🙂🙃", Count: 1},
	}}
	real := api.Secret("κ_αβ.🙂")
	value, err := makeSentinelWithRead(context.Background(), "TOKEN", &real, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(2, 0, 1))
	if err != nil || value != "κ_γα.🙃" || len(value) != len(real) {
		t.Fatal("sentinel changed its exact prefix, separator or UTF8 byte shape")
	}
	if err := checkSentinelValue(context.Background(), "TOKEN", value, shape); err != nil {
		t.Fatal("generated sentinel no longer matches its source format")
	}
	if err := checkSentinelValue(context.Background(), "TOKEN", "κ_αβ.🙃", shape); err != nil {
		t.Fatal("matching changed-value shape was refused")
	}
	if err := checkSentinelValue(context.Background(), "TOKEN", "κ_ab.🙃", shape); err == nil {
		t.Fatal("different byte-width value silently changed an existing shape")
	}
}

func TestSentinelAllPunctuationWorksOnlyWithARepresentableAlternative(t *testing.T) {
	real := api.Secret("!!")
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "!?", Count: 2}}}
	value, err := makeSentinelWithRead(context.Background(), "TOKEN", &real, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(1, 0))
	if err != nil || value != "?!" {
		t.Fatal("representable punctuation alphabet was broadened or rejected")
	}
	reads := 0
	_, err = makeSentinelWithRead(context.Background(), "TOKEN", &real, sentinelShape{Slots: []sentinelSlot{{Alphabet: "!", Count: 2}}}, func(api.Secret) bool { return true }, func(context.Context, []byte) error { reads++; return nil })
	if err == nil || reads != 0 {
		t.Fatal("all-singleton shape consumed randomness or pretended a distinct sentinel existed")
	}
}

func TestSentinelChoiceRejectsOutOfDomainEntropyWithoutModuloBias(t *testing.T) {
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "ABC", Count: 1}}}
	value, err := makeSentinelWithRead(context.Background(), "TOKEN", nil, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(3, 3, 1))
	if err != nil || value != "B" {
		t.Fatal("out-of-domain entropy was reduced modulo the alphabet instead of rejected")
	}
}

func TestSentinelCollisionAndEntropyBudgetsAreFinite(t *testing.T) {
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}
	real := api.Secret("A")
	attempts, reads := 0, 0
	read := func(_ context.Context, p []byte) error {
		reads++
		for i := range p {
			p[i] = 1
		}
		return nil
	}
	value, err := makeSentinelWithRead(context.Background(), "TOKEN", &real, shape, func(api.Secret) bool { attempts++; return false }, read)
	if err == nil || value != "" || attempts != maxSentinelAttempts || reads != 1 {
		t.Fatal("collision exhaustion was unbounded or exposed a rejected candidate")
	}
	reads = 0
	_, err = makeSentinelWithRead(context.Background(), "TOKEN", nil, sentinelShape{Slots: []sentinelSlot{{Alphabet: "ABC", Count: 1}}}, func(api.Secret) bool { t.Error("invalid entropy became a candidate"); return true }, func(_ context.Context, p []byte) error {
		reads++
		for i := range p {
			p[i] = 3
		}
		return nil
	})
	if err == nil || reads != 1 {
		t.Fatal("pathological entropy rejection was not bounded inside a batch")
	}
}

func TestSentinelReadFailureDiscardsPartialCandidateAndSensitiveCause(t *testing.T) {
	real := api.Secret("pk_distinct-secret-value")
	shape := sentinelShape{Prefix: "pk_", Slots: []sentinelSlot{{Alphabet: "abcdefghijklmnopqrstuvwxyz-", Count: len(real) - 3}}}
	calls := 0
	value, err := makeSentinelWithRead(context.Background(), "TOKEN", &real, shape, func(api.Secret) bool { calls++; return true }, func(_ context.Context, p []byte) error {
		copy(p, []byte{1, 2, 3})
		return fmt.Errorf("reader included %s: %w", string(real), io.ErrUnexpectedEOF)
	})
	var public *proto.Error
	if value != "" || err == nil || calls != 0 || !errors.As(err, &public) || public.Hint == "" || strings.Contains(err.Error(), string(real)) || strings.Contains(fmt.Sprintf("%+v", err), string(real)) {
		t.Fatal("read failure leaked its cause/partial candidate or lacked a public hint")
	}
}

func TestSentinelCancellationBeforeAfterAndDuringRead(t *testing.T) {
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}
	t.Run("before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reads := 0
		value, err := makeSentinelWithRead(ctx, "TOKEN", nil, shape, func(api.Secret) bool { t.Error("cancelled generation queried registry"); return true }, func(context.Context, []byte) error { reads++; return nil })
		if value != "" || !errors.Is(err, context.Canceled) || reads != 0 {
			t.Fatal("pre-cancelled generation read or returned a candidate")
		}
	})
	t.Run("after-read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		value, err := makeSentinelWithRead(ctx, "TOKEN", nil, shape, func(api.Secret) bool { t.Error("cancelled entropy queried registry"); return true }, func(_ context.Context, p []byte) error { clear(p); cancel(); return nil })
		if value != "" || !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation after a successful read returned a candidate")
		}
	})
	t.Run("during-controlled-read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered := make(chan struct{})
		type outcome struct {
			value api.Secret
			err   error
		}
		done := make(chan outcome, 1)
		go func() {
			value, err := makeSentinelWithRead(ctx, "TOKEN", nil, shape, func(api.Secret) bool { return true }, func(ctx context.Context, _ []byte) error { close(entered); <-ctx.Done(); return ctx.Err() })
			done <- outcome{value, err}
		}()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("controlled entropy read did not start")
		}
		cancel()
		select {
		case got := <-done:
			if got.value != "" || !errors.Is(got.err, context.Canceled) {
				t.Fatal("controlled read cancellation returned success")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("owned generation call did not return on cancellation")
		}
	})
}

func TestSentinelCancellationInAvailabilityCannotPublishCandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: "AB", Count: 1}}}
	value, err := makeSentinelWithRead(ctx, "TOKEN", nil, shape, func(api.Secret) bool { cancel(); return true }, scriptedSentinelRead(1))
	if value != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation at registry validation returned a usable candidate")
	}
}
