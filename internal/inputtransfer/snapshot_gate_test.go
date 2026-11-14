package inputtransfer

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSelectedSnapshotFailureStopsBeforePublicationGateAndPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	plan, scope := transferPlan(t, []byte("sealed bytes"))
	options, _ := receiverFixture(t)
	options.Scope = scope
	var ready atomic.Bool
	options.BeforeCreate = func(context.Context, *os.Root) error { ready.Store(true); return nil }
	client, guest := streamPair(t)
	done := make(chan error, 1)
	go func() { done <- completeFixture(ctx, guest, options) }()
	changed := errors.New("selected snapshot changed")
	checks := 0
	published, payload := false, false
	err := Send(ctx, client, scope, plan, Sources{
		ValidatePayload: func(context.Context) error {
			checks++
			if !ready.Load() {
				t.Error("snapshot checked before actual readiness")
			}
			return changed
		},
		BeforePayload: func(_ context.Context, s Stream) (Stream, error) { published = true; return s, nil },
		File:          func(context.Context, api.InputFile, io.Writer) error { payload = true; return nil },
	}, options.CopyMax, options.SeedMax)
	<-done
	if !errors.Is(err, changed) || checks != 1 || published || payload {
		t.Fatalf("snapshot failure: %v checks=%d gate=%v payload=%v", err, checks, published, payload)
	}
}
