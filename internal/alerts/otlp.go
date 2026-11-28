package alerts

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/version"
)

// OTLP/HTTP JSON log record wire types (OpenTelemetry Logs OTLP/HTTP JSON
// specification, isolation spec 17.6, decision 0031, reuse.md section 17).

type otlpLogsRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeLogs struct {
	Scope      otlpScope       `json:"scope"`
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpLogRecord struct {
	TimeUnixNano         string         `json:"timeUnixNano"`
	ObservedTimeUnixNano string         `json:"observedTimeUnixNano"`
	SeverityNumber       int            `json:"severityNumber"`
	SeverityText         string         `json:"severityText"`
	Body                 otlpAnyValue   `json:"body"`
	Attributes           []otlpKeyValue `json:"attributes"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

func otlpStr(k, v string) otlpKeyValue {
	return otlpKeyValue{Key: k, Value: otlpAnyValue{StringValue: &v}}
}

func otlpInt(k string, v int) otlpKeyValue {
	s := strconv.Itoa(v)
	return otlpKeyValue{Key: k, Value: otlpAnyValue{IntValue: &s}}
}

func otlpSeverity(kind string) (int, string) {
	switch kind {
	case Failed, OOM, DiskFull, SandboxError:
		return 17, "ERROR"
	case NeedsInput, Memory, LinkSilent, ModelBudget, EgressBlocked, HealthDegraded:
		return 13, "WARN"
	default:
		return 9, "INFO"
	}
}

// OTLPJSON encodes ev as one OpenTelemetry OTLP/HTTP JSON ExportLogsServiceRequest
// payload ready to POST with Content-Type: application/json.
func (ev Event) OTLPJSON() []byte {
	ts := ev.at
	if ev.Time != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, ev.Time); err == nil {
			ts = parsed
		}
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	ns := strconv.FormatInt(ts.UTC().UnixNano(), 10)
	sevNum, sevText := otlpSeverity(ev.Event)

	prefix := brand.Name + "."
	attrs := []otlpKeyValue{
		otlpStr(prefix+"event", ev.Event),
		otlpInt(prefix+"event.version", ev.V),
	}
	if ev.Session.ID != "" {
		attrs = append(attrs, otlpStr(prefix+"session.id", ev.Session.ID))
	}
	if ev.Session.Name != "" {
		attrs = append(attrs, otlpStr(prefix+"session.name", ev.Session.Name))
	}
	if ev.Session.Agent != "" {
		attrs = append(attrs, otlpStr(prefix+"session.agent", ev.Session.Agent))
	}
	if ev.Session.Dir != "" {
		attrs = append(attrs, otlpStr(prefix+"session.dir", ev.Session.Dir))
	}
	if ev.Reason != "" {
		attrs = append(attrs, otlpStr(prefix+"reason", ev.Reason))
	}
	if ev.ExitCode != nil {
		attrs = append(attrs, otlpInt(prefix+"exit_code", *ev.ExitCode))
	}
	if ev.Signal != "" {
		attrs = append(attrs, otlpStr(prefix+"signal", ev.Signal))
	}
	if ev.Detail != "" {
		attrs = append(attrs, otlpStr(prefix+"detail", ev.Detail))
		if ev.DetailFrom != "" {
			attrs = append(attrs, otlpStr(prefix+"detail_from", ev.DetailFrom))
		}
	}

	bodyText := ev.Summary
	resAttrs := []otlpKeyValue{
		otlpStr("service.name", brand.Name),
	}
	if ev.Host != "" {
		resAttrs = append(resAttrs, otlpStr("host.name", ev.Host))
	}
	req := otlpLogsRequest{
		ResourceLogs: []otlpResourceLogs{{
			Resource: otlpResource{Attributes: resAttrs},
			ScopeLogs: []otlpScopeLogs{{
				Scope: otlpScope{
					Name:    brand.Name + ".alerts",
					Version: version.Version,
				},
				LogRecords: []otlpLogRecord{{
					TimeUnixNano:         ns,
					ObservedTimeUnixNano: ns,
					SeverityNumber:       sevNum,
					SeverityText:         sevText,
					Body:                 otlpAnyValue{StringValue: &bodyText},
					Attributes:           attrs,
				}},
			}},
		}},
	}
	b, _ := json.Marshal(req)
	return b
}
