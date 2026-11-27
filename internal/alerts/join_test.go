package alerts

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestDispatcherJoinDoesNotConvertCloseGraceIntoWorkerExit(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	d, _ := settleFixture(t, func(request *http.Request) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-request.Context().Done():
			return request.Context().Err()
		}
	})
	t.Cleanup(func() { once.Do(func() { close(release) }); d.Close(time.Second) })
	d.Notify(Event{Event: Started})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the actual accepted delivery did not start")
	}
	d.Close(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Join(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("bounded Close was treated as actual worker exit", err)
	}
	joined := make(chan error, 1)
	go func() { joined <- d.Join(context.Background()) }()
	select {
	case err := <-joined:
		t.Fatal("dispatcher joined before its actual delivery returned", err)
	default:
	}
	once.Do(func() { close(release) })
	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("actual dispatcher owners did not join")
	}
}
