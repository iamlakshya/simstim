package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestJSONObjectProjectionMatchesExistingBarrier(t *testing.T) {
	known := []string{"schema", "host", "sessions", "session_id", "ssh_agent", "policy_sources"}
	inputs := [][]byte{
		[]byte(`{}`), []byte(` { "schema" : 2, "host": "box" } `),
		[]byte(`{"Future":{"x":1,"x":2},"future":null,"number":1e999999}`),
		[]byte(`{"schema":1,"schema":2}`), []byte(`{"schema":1,"\u0073chema":2}`),
		[]byte(`{"Schema":1}`), []byte(`{"\u017fession_id":"ab"}`),
		[]byte(`{"host":null,"ssh_agent":null,"policy_sources":{"future":true}}`),
		[]byte(`{"a":"\ud800","\ud800":1,"\ufffd":2}`),
		[]byte(`{"bad":"\x00"}`), []byte(`{"bad":[1,]}`), []byte(`{"a":true} false`),
		[]byte(`null`), []byte(`[]`), []byte(`{"a":01}`), []byte(`{"a":1.}`),
		[]byte(`{"a":"unterminated}`), []byte(`{"a":-}`),
		append([]byte{'{', '"', 0xff, '"', ':', '1', ','}, []byte(`"\ufffd":2}`)...),
	}
	for index, input := range inputs {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			want, originalErr := referenceJSONObjectFields(input, known...)
			got, projectedErr := ProjectedJSONObjectFields(t.Context(), input, known...)
			if (originalErr == nil) != (projectedErr == nil) || originalErr == nil && !reflect.DeepEqual(want, got) {
				t.Fatalf("object barrier changed: original=%v projected=%v fieldsEqual=%v", originalErr, projectedErr, reflect.DeepEqual(want, got))
			}
			if originalErr != nil && !errors.Is(projectedErr, ErrJSONObject) {
				t.Fatal("materialising adapter lost the canonical object error")
			}
		})
	}
}

func TestJSONProjectionDecodedStringsMatchEncodingJSON(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`""`), []byte(`"a\n\b\t\f\r\/\\\"z"`), []byte(`"\ud800\udc00"`),
		[]byte(`"\ud800x\udc00\ufffd"`), []byte{'"', 0xff, 0xc0, 0x80, '"'},
		[]byte(`"é\u0061𐀀"`), []byte(`"\ud800\nnext"`),
	} {
		var want string
		if err := json.Unmarshal(input, &want); err != nil {
			t.Fatal(err)
		}
		projection, err := NewJSONProjection(t.Context(), bytes.NewReader(input), int64(len(input)))
		if err != nil {
			t.Fatal(err)
		}
		span, err := projection.Root()
		if err != nil {
			t.Fatal(err)
		}
		var got strings.Builder
		if err := projection.WalkString(span, func(r rune) error { got.WriteRune(r); return nil }); err != nil || got.String() != want {
			t.Fatalf("decoded string differs: %q want %q error=%v", got.String(), want, err)
		}
		for _, comparison := range []string{want, "\xff", want + "extra"} {
			equal, err := projection.StringEqual(span, comparison)
			if err != nil || equal != (want == comparison) {
				t.Fatalf("decoded Go string equality changed for %q: %v, %v", comparison, equal, err)
			}
		}
	}
}

func projectionWideObject(duplicate bool) []byte {
	var source strings.Builder
	source.WriteByte('{')
	for i := range 140 {
		if i != 0 {
			source.WriteByte(',')
		}
		fmt.Fprintf(&source, "%q:%d", fmt.Sprintf("field%03d", 139-i), i)
	}
	if duplicate {
		source.WriteString(`,"field\u003130":false`)
	}
	source.WriteByte('}')
	return []byte(source.String())
}

func TestJSONProjectionExactDuplicatesBeyondTheAssistanceTable(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		input := projectionWideObject(duplicate)
		want, referenceErr := referenceJSONObjectFields(input)
		got, materialisingErr := ProjectedJSONObjectFields(t.Context(), input)
		if (referenceErr == nil) != (materialisingErr == nil) || referenceErr == nil && !reflect.DeepEqual(want, got) {
			t.Fatalf("wide materialising barrier differs: reference=%v projected=%v", referenceErr, materialisingErr)
		}
		for _, collide := range []bool{false, true} {
			projection, err := NewJSONProjection(t.Context(), bytes.NewReader(input), int64(len(input)))
			if err != nil {
				t.Fatal(err)
			}
			if collide {
				projection.testNameHash = func(JSONSpan) ([32]byte, error) { return [32]byte{}, nil }
			}
			span, err := projection.Root()
			if err == nil {
				err = projection.Object(span, nil, func(JSONField) error { return nil })
			}
			if (err != nil) != duplicate {
				t.Fatalf("duplicate=%v forcedCollision=%v error=%v", duplicate, collide, err)
			}
		}
	}
}

func TestJSONProjectionMaterialisedNameWitnessDoesNotUseRescans(t *testing.T) {
	input := projectionWideObject(false)
	p, err := NewJSONBytesProjection(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	root, err := p.ObjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	p.testNameHash = func(JSONSpan) ([32]byte, error) {
		t.Fatal("materialised exact names invoked the bounded-reader hash assistance")
		return [32]byte{}, ErrJSONProjection
	}
	names := map[string]bool{}
	var current string
	err = p.object(root, nil, func(span JSONSpan) error {
		var value strings.Builder
		if err := p.WalkString(span, func(r rune) error { value.WriteRune(r); return nil }); err != nil {
			return err
		}
		current = value.String()
		if names[current] {
			return ErrJSONProjection
		}
		return nil
	}, func(JSONField) error { names[current] = true; return nil })
	if err != nil || len(names) != 140 {
		t.Fatalf("materialised names=%d error=%v", len(names), err)
	}
}

func TestJSONProjectionPreservesExistingNestingBoundary(t *testing.T) {
	for _, depth := range []int{9999, 10000, 10001} {
		input := []byte(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
		projection, err := NewJSONProjection(t.Context(), bytes.NewReader(input), int64(len(input)))
		if err == nil {
			_, err = projection.Root()
		}
		if (err == nil) != json.Valid(input) {
			t.Fatalf("depth %d changed syntax acceptance: %v", depth, err)
		}
		object := append([]byte(`{"unknown":`), input...)
		object = append(object, '}')
		_, originalErr := referenceJSONObjectFields(object)
		_, projectedErr := ProjectedJSONObjectFields(t.Context(), object)
		if (originalErr == nil) != (projectedErr == nil) {
			t.Fatalf("Token/RawMessage object depth %d changed: original=%v projected=%v", depth, originalErr, projectedErr)
		}
	}
}

type projectionCancelReader struct {
	reader io.ReaderAt
	cancel context.CancelFunc
	reads  int
}

func (r *projectionCancelReader) ReadAt(p []byte, offset int64) (int, error) {
	r.reads++
	if r.reads == 2 {
		r.cancel()
	}
	return r.reader.ReadAt(p, offset)
}

func TestJSONProjectionCancellationInterruptsLargeSkippedValues(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := []byte(`{"opaque":"` + strings.Repeat("x", 4<<20) + `"}`)
	reader := &projectionCancelReader{reader: bytes.NewReader(input), cancel: cancel}
	projection, err := NewJSONProjection(ctx, reader, int64(len(input)))
	if err == nil {
		_, err = projection.Root()
	}
	if !errors.Is(err, context.Canceled) || reader.reads > 2 {
		t.Fatalf("cancelled skipped body kept reading: reads=%d error=%v", reader.reads, err)
	}
}

func FuzzJSONObjectProjectionParity(f *testing.F) {
	for _, seed := range []string{`{}`, `{"level":"none"}`, `{"x":{"a":1,"a":2}}`, `{"Schema":2}`, `{"\ud800":1,"\ufffd":2}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		want, originalErr := referenceJSONObjectFields(input, "schema", "level", "exposed")
		got, projectedErr := ProjectedJSONObjectFields(t.Context(), input, "schema", "level", "exposed")
		if (originalErr == nil) != (projectedErr == nil) || originalErr == nil && !reflect.DeepEqual(want, got) {
			t.Fatalf("projector changed the existing object barrier: original=%v projected=%v", originalErr, projectedErr)
		}
	})
}
