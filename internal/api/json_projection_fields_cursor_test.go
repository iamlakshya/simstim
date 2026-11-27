package api

import (
	"strings"
	"testing"
)

func TestRawFieldCursorPreservesEveryOccurrenceWithoutWeakeningObjectBarrier(t *testing.T) {
	const raw = `{"B":1,"A":2,"B":3}`
	p, err := NewJSONProjection(t.Context(), strings.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	root, err := p.Root()
	if err != nil {
		t.Fatal(err)
	}
	fields, err := p.Fields(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"B", "A", "B"} {
		field, exists, err := fields.Next()
		if err != nil || !exists {
			t.Fatal("source occurrence was lost", err)
		}
		equal, err := p.StringEqual(field.Name, name)
		if err != nil || !equal {
			t.Fatal("source key order changed", err)
		}
	}
	if _, exists, err := fields.Next(); err != nil || exists {
		t.Fatal("raw field cursor did not end exactly", err)
	}
	if err := p.Object(root, nil, func(JSONField) error { return nil }); err == nil {
		t.Fatal("raw cursor weakened the ordinary duplicate barrier")
	}
	var zero JSONFields
	if _, _, err := zero.Next(); err == nil {
		t.Fatal("unowned raw cursor was accepted")
	}
}
