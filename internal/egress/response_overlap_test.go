package egress

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func responseCrossingEchoView(t *testing.T, unresolved bool) *responseEchoSnapshot {
	t.Helper()
	if !unresolved {
		return responseTestEchoes(t, responseEchoPair{Value: "ABCD1234", Sentinel: "abcdefgh"}, responseEchoPair{Value: "1234WXYZ", Sentinel: "ijklmnop"})
	}
	base, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{
		{Value: "ABCD1234", Choices: []responseEchoChoice{{Identity: "first", Sentinel: "abcdefgh"}}},
		{Value: "1234WXYZ", Choices: []responseEchoChoice{{Identity: "other-a", Sentinel: "ijklmnop"}, {Identity: "other-b", Sentinel: "qrstuvwx"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := base.view([]string{"first"})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestResponseCrossingKnownEchoesWithholdTheWholeSourceAcrossEverySplit(t *testing.T) {
	const input = "ABCD1234WXYZ"
	for _, unresolved := range []bool{false, true} {
		view := responseCrossingEchoView(t, unresolved)
		for split := 0; split <= len(input)+1; split++ {
			pieces := []string{}
			if split > len(input) {
				for i := range len(input) {
					pieces = append(pieces, input[i:i+1])
				}
			} else {
				pieces = []string{input[:split], input[split:]}
			}
			reader, err := newResponseCredentialReader(t.Context(), &responsePieceReader{pieces: pieces}, func(context.Context) (*responseEchoSnapshot, error) { return view, nil }, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr == nil || len(got) != 0 {
				t.Fatal("crossing known echo source or its first replacement escaped before overlap refusal")
			}
		}
	}
}

type responseOverlapCounter struct {
	io.Reader
	total int
}

func (c *responseOverlapCounter) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.total += n
	return n, err
}
func (*responseOverlapCounter) Close() error { return nil }

func TestResponseCrossingLookaheadBudgetIsDerivedFromTwoRegisteredSpans(t *testing.T) {
	const size = 64 << 10
	first := "A" + strings.Repeat("x", size-5) + "1234"
	second := "1234" + strings.Repeat("y", size-4)
	tail := strings.Repeat("y", size-5) + "z" // last byte disproves the entire second span
	view := responseTestEchoes(t, responseEchoPair{Value: api.Secret(first), Sentinel: api.Secret(strings.Repeat("a", size))}, responseEchoPair{Value: api.Secret(second), Sentinel: api.Secret(strings.Repeat("b", size))})
	source := &responseOverlapCounter{Reader: strings.NewReader(first + tail)}
	reader, err := newResponseCredentialReader(t.Context(), source, func(context.Context) (*responseEchoSnapshot, error) { return view, nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	initial := make([]byte, 1)
	if n, err := reader.Read(initial); err != nil || n != 1 || initial[0] != 'a' || source.total != len(first)+len(tail) {
		t.Fatal("first span was emitted early or the mechanically required second-span carry was refused")
	}
	if cap(reader.buffer) > 2*size+responsePatternWindow+responseReadBuffer {
		t.Fatal("crossing lookahead exceeded its derived carry bound")
	}
	rest, err := io.ReadAll(reader)
	if err != nil || string(initial)+string(rest) != strings.Repeat("a", size)+tail {
		t.Fatal("decided non-crossing lookalike changed bytes or failed after adequate lookahead")
	}
}

func TestResponseCrossingPrefixCancellationAndBackpressure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		input, writer := io.Pipe()
		view := responseCrossingEchoView(t, true)
		reader, err := newResponseCredentialReader(ctx, input, func(context.Context) (*responseEchoSnapshot, error) { return view, nil }, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); _ = input.Close(); _ = writer.Close(); _ = reader.Close() })
		result := make(chan struct {
			data []byte
			err  error
		}, 1)
		go func() {
			p := make([]byte, 12)
			n, err := reader.Read(p)
			result <- struct {
				data []byte
				err  error
			}{p[:n], err}
		}()
		written := make(chan error, 1)
		go func() { _, err := io.WriteString(writer, "ABCD1234"); written <- err }()
		select {
		case err := <-written:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("owned source never reached its known overlapping prefix")
		}
		select {
		case <-result:
			t.Fatal("known or unresolved crossing prefix escaped before another source decision")
		default:
		}
		if cancelled {
			cancel()
		} else {
			go func() { _, err := io.WriteString(writer, "WXYY"); written <- err }()
		}
		select {
		case got := <-result:
			if cancelled {
				if len(got.data) != 0 || !errors.Is(got.err, context.Canceled) {
					t.Fatal("cancelled overlapping carry exposed data or became cleanEOF")
				}
			} else if got.err != nil || string(got.data) != "abcdefghWXYY" {
				t.Fatal("decided lookalike waited for originEOF or changed output")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("owned overlapping source read failed to leave cancellation/backpressure")
		}
		if !cancelled {
			select {
			case err := <-written:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("owned lookalike writer outlived its read")
			}
		}
		cancel()
		_ = reader.Close()
		_ = writer.Close()
	}
}
