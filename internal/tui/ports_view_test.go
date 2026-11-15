package tui

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestForwardSummaryUsesOnlyBoundedPortNumbers(t *testing.T) {
	got := forwardSummary([]api.PortForwardInfo{{Port: 3000}, {Port: 5432, Reverse: true}, {Port: -1}, {Port: 65536}})
	if got != "port 3000, reverse 5432" {
		t.Fatalf("forward summary = %q", got)
	}
}
