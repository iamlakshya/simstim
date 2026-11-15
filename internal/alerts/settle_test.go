package alerts

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type settleClock struct {
	*fakeClock
	scheduled chan time.Duration
}

func (c *settleClock) AfterFunc(d time.Duration, f func()) func() bool {
	stop := c.fakeClock.AfterFunc(d, f)
	c.scheduled <- d
	return stop
}

func (c *settleClock) wait(t *testing.T, want time.Duration) {
	t.Helper()
	for {
		select {
		case got := <-c.scheduled:
			if got == want {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no actual %s deadline was scheduled", want)
		}
	}
}

func settleFixture(t *testing.T, send func(*http.Request) error) (*Dispatcher, *settleClock) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "alerts.toml")
	if err := Save(path, api.AlertsConfig{Events: []string{Started, Restored, NeedsInput}, Delay: "10s", Destinations: []api.AlertDestination{{Type: "webhook", URL: "http://localhost/events"}}}); err != nil {
		t.Fatal(err)
	}
	clock := &settleClock{fakeClock: newFakeClock(), scheduled: make(chan time.Duration, 16)}
	client := &http.Client{Transport: collectorTransport(func(r *http.Request) (*http.Response, error) {
		if err := send(r); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	d := New(Options{Path: path, Host: "box", Clock: clock, HTTP: client, Limiter: NewFileLimiter(filepath.Join(root, "rate")), Log: &DeliveryLog{Path: filepath.Join(root, "log"), Lock: filepath.Join(root, "rate")}})
	t.Cleanup(func() { d.Close(time.Second) })
	return d, clock
}

func waitSettleResult(t *testing.T, result <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-result:
		if got != want {
			t.Fatalf("settled=%t, want %t", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the actual settlement did not finish")
	}
}

func TestSettleJoinsTheActualAcceptedDelivery(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	d, clock := settleFixture(t, func(r *http.Request) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-r.Context().Done():
			return r.Context().Err()
		}
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	d.Notify(Event{Event: Started})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted delivery did not start")
	}
	result, finished := make(chan bool, 1), make(chan struct{})
	go func() { defer close(finished); result <- d.Settle(2 * time.Second) }()
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); d.Close(time.Second); <-finished })
	clock.wait(t, 2*time.Second)
	select {
	case got := <-result:
		t.Fatalf("settled before the real delivery joined: %t", got)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	waitSettleResult(t, result, true)
}

func TestSettleDoesNotForceDelayOrExtendItsBudget(t *testing.T) {
	delivered := make(chan struct{}, 1)
	d, clock := settleFixture(t, func(*http.Request) error { delivered <- struct{}{}; return nil })
	d.Notify(Event{Event: NeedsInput})
	clock.wait(t, 10*time.Second)
	result, finished := make(chan bool, 1), make(chan struct{})
	go func() { defer close(finished); result <- d.Settle(2 * time.Second) }()
	t.Cleanup(func() { d.Close(time.Second); <-finished })
	clock.wait(t, 2*time.Second)
	clock.Advance(2 * time.Second)
	waitSettleResult(t, result, false)
	select {
	case <-delivered:
		t.Fatal("settlement forced the ten-second alert delay early")
	default:
	}
}

func TestSettleDoesNotWaitForLaterNotifications(t *testing.T) {
	oldStarted, laterStarted := make(chan struct{}), make(chan struct{})
	releaseOld, releaseLater := make(chan struct{}), make(chan struct{})
	var oldOnce, laterOnce sync.Once
	calls := 0
	d, clock := settleFixture(t, func(r *http.Request) error {
		calls++
		start, release := oldStarted, releaseOld
		if calls == 2 {
			start, release = laterStarted, releaseLater
		}
		close(start)
		select {
		case <-release:
			return nil
		case <-r.Context().Done():
			return r.Context().Err()
		}
	})
	t.Cleanup(func() { oldOnce.Do(func() { close(releaseOld) }); laterOnce.Do(func() { close(releaseLater) }) })
	d.Notify(Event{Event: Started})
	select {
	case <-oldStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the original delivery did not start")
	}
	result, finished := make(chan bool, 1), make(chan struct{})
	go func() { defer close(finished); result <- d.Settle(2 * time.Second) }()
	t.Cleanup(func() {
		oldOnce.Do(func() { close(releaseOld) })
		laterOnce.Do(func() { close(releaseLater) })
		d.Close(time.Second)
		<-finished
	})
	clock.wait(t, 2*time.Second)
	d.Notify(Event{Event: Restored})
	oldOnce.Do(func() { close(releaseOld) })
	select {
	case <-laterStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the later delivery did not start")
	}
	waitSettleResult(t, result, true)
	laterOnce.Do(func() { close(releaseLater) })
}
