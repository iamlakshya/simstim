package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func TestOTLPFormatReachesCollector(t *testing.T) {
	var gotCT, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := api.AlertsConfig{
		Events: []string{SandboxStopped, SandboxReady, SandboxError, EgressBlocked, HealthDegraded, HealthRecovered},
		Destinations: []api.AlertDestination{{
			Type:    "webhook",
			URL:     srv.URL + "/v1/logs",
			Format:  "otlp",
			Headers: map[string]string{"Authorization": "Bearer collector-tok"},
		}},
	}
	set, err := Check(cfg)
	if err != nil {
		t.Fatalf("Check(otlp): %v", err)
	}

	ev := prepare(Event{
		Event:   SandboxStopped,
		Session: Session{ID: "abcd2345", Name: "batch-1", Agent: "claude", Dir: "/work/app"},
		Reason:  "max_run",
		at:      time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC),
	}, "box", set)
	sink := newSink(cfg.Destinations[0], sinkEnv{client: srv.Client()})
	if _, err := sink.send(context.Background(), ev); err != nil {
		t.Fatalf("send otlp: %v", err)
	}

	if gotCT != "application/json" || gotAuth != "Bearer collector-tok" {
		t.Fatalf("headers: content-type=%q auth=%q", gotCT, gotAuth)
	}
	var payload otlpLogsRequest
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal OTLP payload: %v\n%s", err, gotBody)
	}
	if len(payload.ResourceLogs) != 1 || len(payload.ResourceLogs[0].ScopeLogs) != 1 || len(payload.ResourceLogs[0].ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("unexpected OTLP structure: %+v", payload)
	}
	rec := payload.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.TimeUnixNano != "1795860000000000000" {
		t.Errorf("timeUnixNano = %q", rec.TimeUnixNano)
	}
	if rec.SeverityNumber != 9 || rec.SeverityText != "INFO" {
		t.Errorf("severity = (%d, %q), want (9, INFO)", rec.SeverityNumber, rec.SeverityText)
	}
	if rec.Body.StringValue == nil || !strings.Contains(*rec.Body.StringValue, "batch-1 on box sandbox stopped (max_run)") {
		t.Errorf("body = %+v", rec.Body)
	}
	attrs := map[string]string{}
	for _, a := range rec.Attributes {
		if a.Value.StringValue != nil {
			attrs[a.Key] = *a.Value.StringValue
		}
	}
	if attrs[brand.Name+".event"] != SandboxStopped || attrs[brand.Name+".reason"] != "max_run" || attrs[brand.Name+".session.id"] != "abcd2345" {
		t.Errorf("unexpected OTLP attributes: %+v", attrs)
	}
}

func TestP116AlertEventsAndDetailFromGuest(t *testing.T) {
	for _, kind := range []string{EgressBlocked, SandboxReady, SandboxStopped, SandboxError, HealthDegraded, HealthRecovered} {
		if !slices.Contains(Kinds, kind) {
			t.Errorf("%s missing from Kinds", kind)
		}
		if !slices.Contains(DefaultEvents, kind) {
			t.Errorf("%s missing from DefaultEvents", kind)
		}
		ev, err := TestEvent(kind, "/work/app")
		if err != nil {
			t.Fatalf("TestEvent(%s): %v", kind, err)
		}
		set, err := Check(api.AlertsConfig{})
		if err != nil {
			t.Fatalf("Check default: %v", err)
		}
		ev = prepare(ev, "box", set)
		if ev.Summary == "" {
			t.Errorf("empty summary for %s", kind)
		}
	}

	ev := Event{
		Event:      NeedsInput,
		Session:    Session{ID: "abcd2345", Name: "app"},
		Detail:     "Allow bash?",
		DetailFrom: "guest",
		Reason:     "prompt",
	}
	env := ev.env()
	if !slices.Contains(env, brand.Env("DETAIL_FROM")+"=guest") {
		t.Errorf("env missing DETAIL_FROM=guest: %v", env)
	}
	if !slices.Contains(env, brand.Env("REASON")+"=prompt") {
		t.Errorf("env missing REASON=prompt: %v", env)
	}
}
