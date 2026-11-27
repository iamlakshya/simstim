package egress

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

type responsePieceReader struct {
	pieces []string
	offset int
}

func (r *responsePieceReader) Close() error { return nil }
func (r *responsePieceReader) Read(p []byte) (int, error) {
	for len(r.pieces) > 0 {
		piece := r.pieces[0]
		if r.offset == len(piece) {
			r.pieces = r.pieces[1:]
			r.offset = 0
			continue
		}
		n := copy(p, piece[r.offset:])
		r.offset += n
		return n, nil
	}
	return 0, io.EOF
}

func responseTestRules(t *testing.T) *responseRuleSnapshot {
	t.Helper()
	rules, err := newResponseRuleSnapshot([]responseMintRule{{ID: "fixture-finite-v1", Expression: `\b(tok_[A-Z]{6})(?:["'\s;]|\\[nr]|$)`}})
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func responseTestEchoes(t *testing.T, pairs ...responseEchoPair) *responseEchoSnapshot {
	t.Helper()
	snapshot, err := newResponseEchoSnapshot(pairs)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestResponseExactEchoesAreLongestAndGeneratedBytesAreTerminal(t *testing.T) {
	pairs := []responseEchoPair{{Value: "ab", Sentinel: "xy"}, {Value: "abcd", Sentinel: "wxyz"}, {Value: "original00", Sentinel: "tok_ABCDEF"}}
	snapshot := responseTestEchoes(t, pairs...)
	pairs[1] = responseEchoPair{Value: "abcd", Sentinel: "leak"} // caller slice is no longer owned by the snapshot
	input := "abcd/ab/original00"
	for split := 0; split <= len(input); split++ {
		calls := 0
		reader, err := newResponseCredentialReader(context.Background(), &responsePieceReader{pieces: []string{input[:split], input[split:]}}, func(context.Context) (*responseEchoSnapshot, error) { return snapshot, nil }, responseTestRules(t), func(context.Context, api.Secret, string) (api.Secret, error) {
			calls++
			return "", errors.New("generated output was rescanned")
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil || string(got) != "wxyz/xy/tok_ABCDEF" || calls != 0 {
			t.Fatalf("split%d longest/provenance failure, bytes=%d calls=%d error=%v", split, len(got), calls, err)
		}
	}
}

func TestResponseMintedSplitCaptureDelimiterAndLaterExactEcho(t *testing.T) {
	const value = "tok_ABCDEF"
	const sentinel = "tok_ZZZZZZ"
	input := "before=\"" + value + "\";later=" + value + ":"
	want := "before=\"" + sentinel + "\";later=" + sentinel + ":"
	for split := 0; split <= len(input); split++ {
		var current atomic.Pointer[responseEchoSnapshot]
		current.Store(responseTestEchoes(t))
		calls := 0
		reader, err := newResponseCredentialReader(context.Background(), &responsePieceReader{pieces: []string{input[:split], input[split:]}}, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, responseTestRules(t), func(_ context.Context, candidate api.Secret, id string) (api.Secret, error) {
			calls++
			if candidate != value || id != "fixture-finite-v1" {
				return "", errors.New("wrong original candidate identity")
			}
			current.Store(responseTestEchoes(t, responseEchoPair{Value: candidate, Sentinel: sentinel}))
			return sentinel, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil || string(got) != want || calls != 1 {
			t.Fatalf("split%d failed capture/refresh/delimiters: calls=%d bytes=%d error=%v", split, calls, len(got), err)
		}
	}
}

func TestResponseReadBoundaryIsNotEOFAndContextUsesOriginalBytes(t *testing.T) {
	for _, tc := range []struct {
		pieces []string
		want   string
		calls  int
	}{
		{[]string{"tok_ABCDEF", "A\n"}, "tok_ABCDEFA\n", 0},
		{[]string{"tok_ABCDEF"}, "tok_ZZZZZZ", 1},
		{[]string{"tok_ABCDEF", "\\", "nnext"}, "tok_ZZZZZZ\\nnext", 1},
		{[]string{"x", "tok_ABCDEF\n"}, "xtok_ABCDEF\n", 0},
		{[]string{"X", "tok_ABCDEF\n"}, " tok_ABCDEF\n", 0},
	} {
		var current atomic.Pointer[responseEchoSnapshot]
		current.Store(responseTestEchoes(t, responseEchoPair{Value: "X", Sentinel: " "}))
		calls := 0
		reader, err := newResponseCredentialReader(context.Background(), &responsePieceReader{pieces: tc.pieces}, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, responseTestRules(t), func(_ context.Context, value api.Secret, _ string) (api.Secret, error) {
			calls++
			current.Store(responseTestEchoes(t, responseEchoPair{Value: "X", Sentinel: " "}, responseEchoPair{Value: value, Sentinel: "tok_ZZZZZZ"}))
			return "tok_ZZZZZZ", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil || string(got) != tc.want || calls != tc.calls {
			t.Fatalf("EOF/original-context failure: calls=%d expected=%d bytes=%d error=%v", calls, tc.calls, len(got), err)
		}
	}
}

func TestResponseRefreshesExternallyPublishedEchoes(t *testing.T) {
	var current atomic.Pointer[responseEchoSnapshot]
	current.Store(responseTestEchoes(t))
	reader, err := newResponseCredentialReader(context.Background(), &responsePieceReader{pieces: []string{"!", "newvalue:"}}, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	first := make([]byte, 1)
	if n, err := reader.Read(first); err != nil || n != 1 || string(first) != "!" {
		t.Fatal("first source part failed")
	}
	current.Store(responseTestEchoes(t, responseEchoPair{Value: "newvalue", Sentinel: "safedata"}))
	tail, err := io.ReadAll(reader)
	if err != nil || string(tail) != "safedata:" {
		t.Fatal("canonical external snapshot publication was not refreshed")
	}
}

func TestResponseRegistrationFailureOrUnpublishedSentinelHasNoRawFallback(t *testing.T) {
	for _, mode := range []string{"callback-error", "wrong-length", "same-value", "not-published"} {
		t.Run(mode, func(t *testing.T) {
			snapshot := responseTestEchoes(t)
			reader, err := newResponseCredentialReader(context.Background(), io.NopCloser(strings.NewReader("prefix tok_ABCDEF\n")), func(context.Context) (*responseEchoSnapshot, error) { return snapshot, nil }, responseTestRules(t), func(_ context.Context, value api.Secret, _ string) (api.Secret, error) {
				switch mode {
				case "callback-error":
					return "", errors.New("sensitive callback detail")
				case "wrong-length":
					return "short", nil
				case "same-value":
					return value, nil
				default:
					return "tok_ZZZZZZ", nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr == nil || strings.Contains(string(got), "tok_") || strings.Contains(readErr.Error(), "sensitive") {
				t.Fatal("registration error released candidate bytes, unregistered output or callback details")
			}
		})
	}
}

func TestResponseOverlapsChooseContainingIdentityAndRefuseCrossing(t *testing.T) {
	for _, tc := range []struct {
		value, sentinel, input, want string
		refuse                       bool
	}{
		{"tok_ABCDEF", "same_ABCDE", "tok_ABCDEF\n", "same_ABCDE\n", false},
		{"pre tok_ABCDEF", "safe-safe-safe", "pre tok_ABCDEF\n", "safe-safe-safe\n", false},
		{"pre tok_", "safe0000", "pre tok_ABCDEF\n", "", true},
		{"DEFtail", "SAFEOUT", "tok_ABCDEFtail\n", "tok_ABCSAFEOUT\n", false},
		{"DEF\nextra", "SAFE00000", "tok_ABCDEF\nextra", "", true},
	} {
		snapshot, err := newResponseEchoSnapshot([]responseEchoPair{{Value: api.Secret(tc.value), Sentinel: api.Secret(tc.sentinel)}})
		if err != nil {
			t.Fatal(err)
		}
		reader, err := newResponseCredentialReader(context.Background(), io.NopCloser(strings.NewReader(tc.input)), func(context.Context) (*responseEchoSnapshot, error) { return snapshot, nil }, responseTestRules(t), func(context.Context, api.Secret, string) (api.Secret, error) {
			return "", errors.New("unexpected registration")
		})
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if tc.refuse {
			if readErr == nil || strings.Contains(string(got), "tok_") {
				t.Fatal("crossing credential overlap emitted its candidate")
			}
		} else if readErr != nil || string(got) != tc.want {
			t.Fatal("containing response identity or ordinary exact echo changed")
		}
	}
}

func TestResponseOneByteChunksKeepTheMaximumPatternAndFutureEcho(t *testing.T) {
	rules, err := newResponseRuleSnapshot([]responseMintRule{{ID: "fixture-167-v1", Expression: `\b(fake_[A-Z]{162})(?:[\s;]|\\[nr]|$)`}})
	if err != nil {
		t.Fatal(err)
	}
	value := "fake_" + strings.Repeat("A", 162)
	sentinel := "fake_" + strings.Repeat("B", 162)
	input := "\"" + value + "\\n" + value + ":"
	want := "\"" + sentinel + "\\n" + sentinel + ":"
	var pieces []string
	for i := range len(input) {
		pieces = append(pieces, input[i:i+1])
	}
	var current atomic.Pointer[responseEchoSnapshot]
	current.Store(responseTestEchoes(t))
	calls := 0
	reader, err := newResponseCredentialReader(context.Background(), &responsePieceReader{pieces: pieces}, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, rules, func(_ context.Context, candidate api.Secret, id string) (api.Secret, error) {
		calls++
		if candidate != api.Secret(value) || id != "fixture-167-v1" {
			return "", errors.New("wrong maximum candidate")
		}
		current.Store(responseTestEchoes(t, responseEchoPair{Value: candidate, Sentinel: api.Secret(sentinel)}))
		return api.Secret(sentinel), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var output strings.Builder
	p := make([]byte, 1)
	for {
		n, readErr := reader.Read(p)
		if n > 0 {
			output.Write(p[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(reader.pending()) > reader.maxEcho+responsePatternWindow+responseReadBuffer {
			t.Fatal("original carry exceeded its declared bound")
		}
	}
	if output.String() != want || calls != 1 {
		t.Fatal("maximum one-byte pattern/terminator or newly known echo was lost")
	}
}

type responseInterruptedReader struct{}

func (responseInterruptedReader) Close() error { return nil }
func (responseInterruptedReader) Read(p []byte) (int, error) {
	return copy(p, "tok_ABCDE"), io.ErrUnexpectedEOF
}

func TestResponseUncertainInputEndDoesNotReleaseCandidatePrefix(t *testing.T) {
	reader, err := newResponseCredentialReader(context.Background(), responseInterruptedReader{}, func(context.Context) (*responseEchoSnapshot, error) { return nil, nil }, responseTestRules(t), func(context.Context, api.Secret, string) (api.Secret, error) {
		return "", errors.New("unexpected callback")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, readErr := io.ReadAll(reader)
	if readErr == nil || len(got) != 0 {
		t.Fatal("failed source read was treated as a successful credential EOF")
	}
}
