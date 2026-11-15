package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/proto"
)

const validEventLine = `{"v":1,"event":"needs_input","host":"box","session":{"id":"abcd2345","name":"job"},"time":"2026-11-15T10:00:00Z","summary":"job on box needs you"}` + "\n"

func TestEventsReaderKeepsBufferedRecordsAndExactSession(t *testing.T) {
	var events []alerts.Event
	err := readEvents(t.Context(), strings.NewReader(validEventLine+validEventLine), "abcd2345", func(ev alerts.Event) error { events = append(events, ev); return nil })
	if err != nil || len(events) != 2 || events[0].Session.ID != "abcd2345" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestEventsReaderRefusesInvalidFutureForeignAndIncompleteRecords(t *testing.T) {
	for name, line := range map[string]string{
		"version":         strings.Replace(validEventLine, `"v":1`, `"v":2`, 1),
		"kind":            strings.Replace(validEventLine, "needs_input", "future_event", 1),
		"target":          strings.Replace(validEventLine, "abcd2345", "defg2345", 1),
		"time":            strings.Replace(validEventLine, "2026-11-15T10:00:00Z", "yesterday", 1),
		"missing newline": strings.TrimSuffix(validEventLine, "\n"),
		"oversized":       strings.Replace(validEventLine, "job on box needs you", strings.Repeat("x", proto.MaxFrame), 1),
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			err := readEvents(t.Context(), strings.NewReader(line), "abcd2345", func(alerts.Event) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("invalid event reached callback: called=%t err=%v", called, err)
			}
		})
	}
}

func TestEventsReaderStopsAtCallbackFailureOrCancellation(t *testing.T) {
	want := errors.New("consumer stopped")
	count := 0
	err := readEvents(t.Context(), strings.NewReader(validEventLine+validEventLine), "", func(alerts.Event) error { count++; return want })
	if !errors.Is(err, want) || count != 1 {
		t.Fatalf("callback count=%d err=%v", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = readEvents(ctx, strings.NewReader(validEventLine), "", func(alerts.Event) error { t.Fatal("canceled reader delivered an event"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
