package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode"
)

type checkpointReadCounter struct {
	*bytes.Reader
	bytes int64
}

func (r *checkpointReadCounter) ReadAt(out []byte, offset int64) (int, error) {
	n, err := r.Reader.ReadAt(out, offset)
	r.bytes += int64(n)
	return n, err
}

func TestProjectionRuneCheckpointsKeepEscapesAndNativeDecodedText(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`"\u2003\t ExAmPle.com:443\u00a0"`),
		[]byte(`"\uD83D\uDE00\uD800X\uDC00"`),
		append(append([]byte{'"'}, []byte{0xff, 0xe2, 0x82}...), '"'),
	} {
		p, err := NewJSONBytesProjection(t.Context(), raw)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		var got strings.Builder
		previous := int64(1)
		err = p.WalkStringSpans(JSONSpan{Start: 0, End: int64(len(raw))}, func(r rune, encoded JSONSpan) error {
			if encoded.Start != previous || encoded.End <= encoded.Start {
				t.Fatal("decoded checkpoints skipped or overlapped source bytes")
			}
			previous = encoded.End
			_, _ = got.WriteRune(r)
			return nil
		})
		if err != nil || got.String() != want || previous != int64(len(raw)-1) {
			t.Fatalf("checkpoint decoder diverged from the native JSON string: %q want %q, %v", got.String(), want, err)
		}
	}
}

func TestProjectionValidatedInteriorDoesNotReplayTrimPadding(t *testing.T) {
	raw := []byte(`"` + strings.Repeat(" ", 500000) + `ExAmPle.com:443` + strings.Repeat(`\u2003`, 80000) + `"`)
	reader := &checkpointReadCounter{Reader: bytes.NewReader(raw)}
	p, err := NewJSONProjection(t.Context(), reader, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	whole := JSONSpan{Start: 0, End: int64(len(raw))}
	var interior JSONSpan
	if err := p.WalkStringSpans(whole, func(r rune, encoded JSONSpan) error {
		if !unicode.IsSpace(r) {
			if interior.End == 0 {
				interior.Start = encoded.Start
			}
			interior.End = encoded.End
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := reader.bytes
	for range 1000 {
		var got strings.Builder
		if err := p.WalkStringEncodedWindow(whole, interior, func(r rune) error { _, err := got.WriteRune(r); return err }); err != nil {
			t.Fatal(err)
		}
		if got.String() != "ExAmPle.com:443" {
			t.Fatal("validated interior changed its exact source text")
		}
	}
	if read := reader.bytes - before; read > 4*4096 {
		t.Fatalf("comparisons replayed unbounded trim padding: %d bytes", read)
	}
	if err := p.WalkStringEncodedWindow(whole, JSONSpan{Start: 0, End: 1}, func(rune) error { return nil }); !errors.Is(err, ErrJSONProjection) {
		t.Fatal("window outside the decoded string was accepted")
	}
}

func TestProjectionPausableArrayUsesTheCanonicalValueScanner(t *testing.T) {
	for _, raw := range []string{`[]`, `[1,{"x":[2]},"text",null]`, `[1,]`, `[1 2]`, `[,1]`} {
		p, err := NewJSONBytesProjection(context.Background(), []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		cursor, err := p.Values(JSONSpan{Start: 0, End: int64(len(raw))})
		var got []json.RawMessage
		for err == nil {
			var value JSONSpan
			var more bool
			value, more, err = cursor.Next()
			if err != nil || !more {
				break
			}
			got = append(got, json.RawMessage(raw[value.Start:value.End]))
		}
		var want []json.RawMessage
		wantErr := json.Unmarshal([]byte(raw), &want)
		if (err == nil) != (wantErr == nil) || err == nil && len(got) != len(want) {
			t.Fatalf("pausable array changed canonical acceptance: %q, %v, %v", raw, err, wantErr)
		}
		if err == nil {
			for index := range want {
				if !bytes.Equal(got[index], want[index]) {
					t.Fatal("pausable array changed an encoded value")
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	p, err := NewJSONProjection(ctx, strings.NewReader(`[1,2]`), 5)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := p.Values(JSONSpan{Start: 0, End: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, more, err := cursor.Next(); err != nil || !more {
		t.Fatal(err)
	}
	cancel()
	if _, _, err := cursor.Next(); !errors.Is(err, context.Canceled) {
		t.Fatal("paused cursor ignored source cancellation:", err)
	}
}
