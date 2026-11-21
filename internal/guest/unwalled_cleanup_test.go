package guest

import (
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
)

func TestNoneDoneJoinsItsOwnedHandleCloseErrors(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer right.Close()
	g := New(left, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.unwalled = &unwalledGuestV1{runtime: root}
	joined := make(chan struct{})
	go func() { g.end(); close(joined) }()
	<-g.Done()
	// Done must synchronise all original cleanup writes, not merely the
	// link close. This also makes an absent handle close observable under race.
	if g.unwalled.cleanupErr == nil {
		t.Fatal("Done preceded its original handle cleanup result")
	}
	<-joined
	g.end() // A second join observes the same once owner and result.
	if g.unwalled.cleanupErr == nil {
		t.Fatal("a repeated end hid its original cleanup failure")
	}
}
