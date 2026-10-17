//go:build !windows

package client_test

import (
	"context"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The host helper's bridge answers RoundTrip's pings, so the launcher times
// the network and the bridge, not a list of sessions.
func TestRoundTripPingsTheBridge(t *testing.T) {
	h := hosttest.New(t)
	ch, _ := h.ClientHost("box")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, ch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range 3 {
		if d, err := c.RoundTrip(ctx); err != nil || d <= 0 {
			t.Fatalf("RoundTrip = %v, %v", d, err)
		}
	}
	if !client.AnswersPings(c) {
		t.Fatal("the bridge did not answer a ping, so RoundTrip timed a list instead")
	}
}
