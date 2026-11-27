package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type collectorTransport func(*http.Request) (*http.Response, error)

func (f collectorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOTLPCollectorReceivesOneLogWithExactIntegers(t *testing.T) {
	code := 42
	ev := prepare(Event{Event: SandboxStopped, Reason: "max_run", ExitCode: &code,
		Session: Session{ID: "abc123", Name: "auth-fix", Agent: "shell", Dir: "/work"},
		at:      time.Unix(1700000000, 0), Detail: "private question"}, "box", &Settings{})
	d := api.AlertDestination{Type: "webhook", URL: "http://localhost:4318/v1/logs", Format: "otlp", Headers: map[string]string{"Authorization": "Bearer local"}}
	if _, err := Check(api.AlertsConfig{Destinations: []api.AlertDestination{d}}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: collectorTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != "POST" || r.URL.String() != d.URL || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer local" {
			t.Fatalf("collector request: %s %s %v", r.Method, r.URL, r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "private question") {
			t.Fatal("detail disabled in the dispatcher reached the collector")
		}
		var export struct {
			Resources []struct {
				Scopes []struct {
					Records []struct {
						Timestamp string `json:"timeUnixNano"`
						Body      struct {
							Text string `json:"stringValue"`
						} `json:"body"`
						Attrs []struct {
							Key   string `json:"key"`
							Value struct {
								Text string `json:"stringValue"`
								Int  string `json:"intValue"`
							} `json:"value"`
						} `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if err := json.Unmarshal(body, &export); err != nil {
			t.Fatal(err)
		}
		if len(export.Resources) != 1 || len(export.Resources[0].Scopes) != 1 || len(export.Resources[0].Scopes[0].Records) != 1 {
			t.Fatalf("collector did not receive one log: %s", body)
		}
		record := export.Resources[0].Scopes[0].Records[0]
		if record.Timestamp != "1700000000000000000" || record.Body.Text != "auth-fix on box stopped (max_run)" {
			t.Fatalf("record lost its time or message: %s", body)
		}
		attrs := map[string]string{}
		for _, a := range record.Attrs {
			attrs[a.Key] = a.Value.Text + a.Value.Int
		}
		if attrs["event.name"] != "sandbox_stopped" || attrs["event.reason"] != "max_run" || attrs["process.exit.code"] != "42" || attrs["session.id"] != "abc123" {
			t.Fatalf("collector attributes: %v", attrs)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	if _, err := newSink(d, sinkEnv{client: client}).send(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("collector received %d requests", requests)
	}
}

func TestOTLPPartialRejectionIsNotRetried(t *testing.T) {
	for _, rejected := range []string{`"1"`, `1`} {
		t.Run(rejected, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: collectorTransport(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"partialSuccess":{"rejectedLogRecords":` + rejected + `,"errorMessage":"bad record"}}`))}, nil
			})}
			s := newSink(api.AlertDestination{Type: "webhook", URL: "http://localhost/v1/logs", Format: "otlp"}, sinkEnv{client: client})
			_, err := deliver(context.Background(), s, testEvent(NeedsInput), 0)
			if err == nil || !strings.Contains(err.Error(), "rejected 1 log records") || requests != 1 {
				t.Fatalf("partial success: requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestLifecycleAlertsUseExistingAdmissionAndNoiseSettings(t *testing.T) {
	for _, kind := range []string{SandboxReady, SandboxStopped, SandboxError, HealthDegraded, HealthRecovered} {
		set, err := Check(api.AlertsConfig{Events: []string{kind}, Destinations: []api.AlertDestination{{Type: "command", Run: "cat"}}})
		if err != nil || !set.wants(kind) || set.Delay != DefaultDelay {
			t.Fatalf("%s did not use alert settings: %v, %v", kind, set, err)
		}
	}
}
