package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// This checks constructor ownership, not the actor's measured heap budget.
// The existing maximum-state and full-actor measurements remain required.
func TestJSONProjectionAllocatesFileWindowsOnlyForReaderAt(t *testing.T) {
	raw := []byte(`{"name":"value"}`)
	memory, err := NewJSONBytesProjection(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if memory.windows != nil || memory.r != nil || len(memory.raw) != len(raw) {
		t.Fatal("in-memory projection acquired file windows or a file reader")
	}
	file, err := NewJSONProjection(t.Context(), bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if file.windows == nil || file.r == nil || file.raw != nil {
		t.Fatal("ReaderAt projection did not own its actual file windows")
	}
	buffered := 0
	for index := range *file.windows {
		buffered += len(file.windows[index].buffer)
	}
	if buffered != 16<<10 {
		t.Fatalf("actual file windows changed: %d bytes", buffered)
	}
	if _, err := file.ObjectRoot(); err != nil {
		t.Fatal(err)
	}
	used := false
	for index := range *file.windows {
		used = used || file.windows[index].n != 0
	}
	if !used || memory.windows != nil {
		t.Fatal("file parsing did not use its windows or raw parsing gained windows")
	}
}

func TestJSONProjectionRawCursorMatchesFileCursor(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(` {"schema":2,"opaque":{"duplicate":1,"duplicate":2},"\u0061":"\ud800"} `),
		projectionWideObject(false), projectionWideObject(true),
		[]byte(`{"Schema":2}`), []byte(`{"bad":[1,]}`),
		append([]byte{'{', '"', 0xff, '"', ':', '1', ','}, []byte(`"\ufffd":2}`)...),
	} {
		memory, err := NewJSONBytesProjection(t.Context(), raw)
		if err != nil {
			t.Fatal(err)
		}
		file, err := NewJSONProjection(t.Context(), bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		var outputs [2][]JSONField
		var failures [2]error
		for index, projection := range []*JSONProjection{memory, file} {
			span, err := projection.ObjectRoot()
			if err == nil {
				err = projection.Object(span, []string{"schema"}, func(field JSONField) error {
					outputs[index] = append(outputs[index], field)
					return nil
				})
			}
			failures[index] = err
		}
		if (failures[0] == nil) != (failures[1] == nil) || failures[0] == nil && !reflect.DeepEqual(outputs[0], outputs[1]) {
			t.Fatalf("cursor mode changed the shared grammar: raw=%v file=%v", failures[0], failures[1])
		}
		if memory.windows != nil {
			t.Fatal("raw parser allocated file windows")
		}
	}
}

func TestProjectedJSONObjectFieldsOwnsNamesAndRawMessages(t *testing.T) {
	raw := []byte(`{"plain": { "nested" : [1,2] },"escaped\u005fname":"value","a":[true],"b":[true]}`)
	fields, err := ProjectedJSONObjectFields(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]json.RawMessage{
		"plain":        []byte(`{ "nested" : [1,2] }`),
		"escaped_name": []byte(`"value"`),
		"a":            []byte(`[true]`),
		"b":            []byte(`[true]`),
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("materialised bytes or decoded names changed: %v", fields)
	}
	for index := range raw {
		raw[index] = 'x'
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatal("returned names or RawMessages retained the mutable input")
	}
	mutatedInput := bytes.Clone(raw)
	fields["a"][1] = 'X'
	if !bytes.Equal(raw, mutatedInput) || !bytes.Equal(fields["b"], want["b"]) {
		t.Fatal("returned values alias the input or another field")
	}
}

func TestJSONBytesProjectionRetainsCancellationWithoutFileWindows(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	projection, err := NewJSONBytesProjection(ctx, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := projection.ObjectRoot(); !errors.Is(err, context.Canceled) || projection.windows != nil {
		t.Fatalf("raw cursor lost cancellation or acquired windows: %v", err)
	}
}
