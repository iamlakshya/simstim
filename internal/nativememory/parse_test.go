package nativememory

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestMeminfoKnownZeroMissingAndCheckedUnits(t *testing.T) {
	valid := "MemTotal: 4096 kB\nMemAvailable: 0 kB\nSwapTotal: 999 kB\n"
	info, err := ReadMeminfo(t.Context(), strings.NewReader(valid), MemTotal, MemAvailable)
	if err != nil {
		t.Fatal(err)
	}
	if total, err := info.Value(MemTotal); err != nil || total != 4096*1024 {
		t.Fatalf("physical total = %d, %v", total, err)
	}
	if available, err := info.Value(MemAvailable); err != nil || available != 0 {
		t.Fatalf("finite zero availability was lost: %d, %v", available, err)
	}
	for _, invalid := range []string{
		"MemTotal: 4096 kB\n",
		"MemAvailable: max\n",
		"MemAvailable: -1 kB\n",
		"MemAvailable: 1 MB\n",
		"MemAvailable: 1 kB\nMemAvailable: 2 kB\n",
		"MemAvailable: 18446744073709551615 kB\n",
	} {
		info, err := ReadMeminfo(t.Context(), strings.NewReader(invalid), MemAvailable)
		if !errors.Is(err, ErrUnknown) {
			t.Fatalf("invalid native field supplied availability: %q, %v", invalid, err)
		}
		if _, err := info.Value(MemAvailable); err == nil {
			t.Fatal("failed field projection became a known zero")
		}
	}
}

func TestNativeLimitDistinguishesUnlimitedAndFiniteZero(t *testing.T) {
	for _, value := range []string{"max", "max\n"} {
		limit, err := ReadLimit(t.Context(), strings.NewReader(value))
		if err != nil || limit.Kind() != Unlimited {
			t.Fatalf("explicit unlimited = %+v, %v", limit, err)
		}
		if _, finite := limit.Bytes(); finite {
			t.Fatal("unlimited was returned as numeric zero")
		}
	}
	zero, err := ReadLimit(t.Context(), strings.NewReader("0\n"))
	if bytes, finite := zero.Bytes(); err != nil || !finite || bytes != 0 {
		t.Fatalf("finite zero = %+v, %v", zero, err)
	}
	for _, invalid := range []string{"", "MAX", "-1", "+1", "0 1", "max 0", "18446744073709551616"} {
		limit, err := ReadLimit(t.Context(), strings.NewReader(invalid))
		if !errors.Is(err, ErrUnknown) || limit.Kind() != Unknown {
			t.Fatalf("invalid limit became capacity: %q, %+v, %v", invalid, limit, err)
		}
	}
	if _, err := ReadScalar(t.Context(), strings.NewReader("max")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("usage accepted an unlimited token: %v", err)
	}
}

type memoryErrorReader struct{ err error }

func (r memoryErrorReader) Read([]byte) (int, error) { return 0, r.err }

type memoryCalledReader struct{ called bool }

func (r *memoryCalledReader) Read([]byte) (int, error) { r.called = true; return 0, io.EOF }

func TestNativeReadersPreserveIOAndCancellation(t *testing.T) {
	marker := errors.New("owned native read failed")
	if _, err := ReadScalar(t.Context(), memoryErrorReader{marker}); !errors.Is(err, marker) {
		t.Fatalf("scalar discarded IO failure: %v", err)
	}
	if _, err := ReadMeminfo(t.Context(), memoryErrorReader{marker}, MemAvailable); !errors.Is(err, marker) {
		t.Fatalf("meminfo discarded IO failure: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := &memoryCalledReader{}
	if _, err := ReadScalar(ctx, reader); !errors.Is(err, context.Canceled) || reader.called {
		t.Fatalf("cancelled scalar read touched its descriptor: %v, %v", reader.called, err)
	}
	if _, err := ReadMeminfo(ctx, reader, MemAvailable); !errors.Is(err, context.Canceled) || reader.called {
		t.Fatalf("cancelled meminfo read touched its descriptor: %v, %v", reader.called, err)
	}
}

func TestAncestorProjectionKeepsEveryLimitAndSaturatesUsage(t *testing.T) {
	unlimited := Limit{kind: Unlimited}
	leaf, err := remaining(Limit{kind: Finite, bytes: 200}, 50)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := remaining(Limit{kind: Finite, bytes: 100}, 90)
	if err != nil {
		t.Fatal(err)
	}
	combined, err := tighter(unlimited, leaf)
	if err != nil {
		t.Fatal(err)
	}
	combined, err = tighter(combined, parent)
	if n, finite := combined.Bytes(); err != nil || !finite || n != 10 {
		t.Fatalf("ancestor limit was ignored: %+v, %v", combined, err)
	}
	zero, err := remaining(Limit{kind: Finite, bytes: 10}, 11)
	if n, finite := zero.Bytes(); err != nil || !finite || n != 0 {
		t.Fatalf("over-limit usage wrapped headroom: %+v, %v", zero, err)
	}
	if _, err := tighter(combined, Limit{}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown ancestor became unlimited: %v", err)
	}
}

func TestSharedMembershipLexerRetainsLegacyRowsAndMarksUnknown(t *testing.T) {
	input := "0::/unit/member\n7:memory,cpu:/legacy/member\nmalformed\nwrong::/still-visible\n8:memory:not-absolute\n"
	membership := ParseMembership([]byte(input))
	if !membership.Malformed || len(membership.Records) != 3 {
		t.Fatalf("shared membership rows = %+v", membership)
	}
	for i, want := range []string{"/unit/member", "/legacy/member", "/still-visible"} {
		if membership.Records[i].Path != want {
			t.Fatalf("legacy path changed: %+v", membership.Records)
		}
	}
	valid := ParseMembership([]byte("0::/\n"))
	if valid.Malformed || len(valid.Records) != 1 || valid.Records[0].Path != "/" {
		t.Fatalf("kernel root membership was guessed or lost: %+v", valid)
	}
}
