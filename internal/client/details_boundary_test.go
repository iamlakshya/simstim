package client_test

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestCleanDetailTrimsOnlyDisplayBoundaries(t *testing.T) {
	for _, row := range []struct{ raw, want string }{
		{"\r\nDebian 13\r\n", "Debian 13"},
		{" left\tright ", "left right"},
		{"first\nsecond", "first second"},
		{"\u202e \u200b", ""},
		{"\x1bc \r\n", "c"},
	} {
		if got := client.CleanDetail(row.raw); got != row.want {
			t.Errorf("CleanDetail(%q) = %q, want %q", row.raw, got, row.want)
		}
	}
	if got := client.CleanDetail(strings.Repeat("x", 2*client.DetailMax)); len([]rune(got)) != client.DetailMax {
		t.Fatalf("display bound changed: %d runes", len([]rune(got)))
	}
}
