package api

import (
	"context"
	"strings"
	"testing"
)

func TestProjectionRuneCursorSharesEscapesAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	const source = `"A\ud83d\ude80\u0000é"`
	p, err := NewJSONProjection(ctx, strings.NewReader(source), int64(len(source)))
	if err != nil {
		t.Fatal(err)
	}
	span, err := p.Root()
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := p.StringRunes(span)
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for {
		r, ok, err := cursor.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got.WriteRune(r)
	}
	if got.String() != "A🚀\x00é" {
		t.Fatal("rune cursor diverged from the existing JSON decoder")
	}
	cursor, err = p.StringRunes(span)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, _, err := cursor.Next(); err == nil {
		t.Fatal("retained rune cursor ignored cancellation")
	}
}
