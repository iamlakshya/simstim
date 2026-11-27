package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/version"
)

// OTLP/HTTP uses protobuf JSON field names and decimal strings for int64s.
// https://opentelemetry.io/docs/specs/otlp/#json-protobuf-encoding
type otlpValue struct {
	String *string `json:"stringValue,omitempty"`
	Int    *string `json:"intValue,omitempty"`
	Bool   *bool   `json:"boolValue,omitempty"`
}

type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

func otlpString(key, value string) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{String: &value}}
}

func otlpBody(ev Event) ([]byte, error) {
	at, err := time.Parse(time.RFC3339Nano, ev.Time)
	if err != nil || at.UnixNano() < 0 {
		return nil, errors.New("the alert has no valid OTLP timestamp")
	}
	attrs := []otlpAttribute{
		otlpString("event.name", ev.Event), otlpString("session.id", ev.Session.ID),
		otlpString("session.name", ev.Session.Name), otlpString("session.agent", ev.Session.Agent),
		otlpString("session.dir", ev.Session.Dir),
	}
	if ev.Reason != "" {
		attrs = append(attrs, otlpString("event.reason", ev.Reason))
	}
	if ev.Signal != "" {
		attrs = append(attrs, otlpString("process.exit.signal", ev.Signal))
	}
	if ev.ExitFrom != "" {
		attrs = append(attrs, otlpString("event.exit_from", ev.ExitFrom))
	}
	if ev.DetailFrom != "" {
		attrs = append(attrs, otlpString("event.detail_from", ev.DetailFrom))
	}
	if ev.Claimed {
		claimed := true
		attrs = append(attrs, otlpAttribute{Key: "event.claimed", Value: otlpValue{Bool: &claimed}})
	}
	if ev.ExitCode != nil {
		code := strconv.Itoa(*ev.ExitCode)
		attrs = append(attrs, otlpAttribute{Key: "process.exit.code", Value: otlpValue{Int: &code}})
	}
	severity, level := 9, "INFO"
	switch ev.Event {
	case Failed, OOM, SandboxError, HealthDegraded, DiskFull, LinkSilent:
		severity, level = 17, "ERROR"
	}
	text := ev.Text()
	record := map[string]any{
		"timeUnixNano": strconv.FormatInt(at.UnixNano(), 10), "severityNumber": severity,
		"severityText": level, "body": otlpValue{String: &text}, "attributes": attrs,
	}
	return json.Marshal(map[string]any{"resourceLogs": []any{map[string]any{
		"resource": map[string]any{"attributes": []otlpAttribute{otlpString("service.name", brand.Name), otlpString("host.name", ev.Host)}},
		"scopeLogs": []any{map[string]any{
			"scope":      map[string]string{"name": brand.Name, "version": version.Version},
			"logRecords": []any{record},
		}},
	}}})
}

type otlpSink struct {
	d      api.AlertDestination
	client *http.Client
}

func (s *otlpSink) send(ctx context.Context, ev Event) (string, error) {
	body, err := otlpBody(ev)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.d.URL, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("the OTLP collector url is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.d.Headers {
		req.Header.Set(k, v)
	}
	return "", postChecked(s.client, req, checkOTLPResponse)
}

func checkOTLPResponse(body []byte) error {
	var result struct {
		Partial *struct {
			Rejected json.RawMessage `json:"rejectedLogRecords"`
			Message  string          `json:"errorMessage"`
		} `json:"partialSuccess"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return errors.New("the OTLP collector returned an invalid JSON response")
	}
	if result.Partial == nil {
		return nil
	}
	var count string
	if len(result.Partial.Rejected) > 0 {
		if json.Unmarshal(result.Partial.Rejected, &count) != nil {
			count = string(result.Partial.Rejected)
		}
		n, err := strconv.ParseUint(count, 10, 64)
		if err != nil {
			return errors.New("the OTLP collector returned an invalid rejected log count")
		}
		if n > 0 {
			return fmt.Errorf("the OTLP collector rejected %d log records: %s", n, oneLine(result.Partial.Message, 200))
		}
	}
	return nil
}
