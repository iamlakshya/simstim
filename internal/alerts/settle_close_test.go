package alerts

import (
	"net/http"
	"testing"
	"time"
)

func TestSettleCloseReportsInterruptedPendingWork(t *testing.T) {
	d, clock := settleFixture(t, func(*http.Request) error { t.Error("close forced an alert out of its delay"); return nil })
	d.Notify(Event{Event: NeedsInput})
	clock.wait(t, 10*time.Second)
	result, finished := make(chan bool, 1), make(chan struct{})
	go func() { defer close(finished); result <- d.Settle(2 * time.Second) }()
	t.Cleanup(func() { d.Close(time.Second); <-finished })
	clock.wait(t, 2*time.Second)
	d.Close(time.Second)
	waitSettleResult(t, result, false)
	if d.Settle(time.Second) {
		t.Fatal("a closed dispatcher claimed a fresh settlement")
	}
}
