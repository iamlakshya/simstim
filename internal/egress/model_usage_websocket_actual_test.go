package egress

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestActualWebSocketUsageWaitsForSuccessfulGuestWritesThenClosesBudget1008(t *testing.T) {
	const request = `{"type":"response.create","event_id":"first","model":"model-a","input":[]}`
	const created = `{"type":"response.created","response":{"id":"response-a"}}`
	const terminal = `{"type":"response.completed","response":{"id":"response-a","usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}}`
	readAhead := make(chan struct{})
	originResult := make(chan error, 1)
	policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Preset: new("locked")}, Network: api.NetworkValues{ModelBudget: new(int64(5))}}}, {Kind: api.LayerSession}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newModelHTTPFixture(t, "codex", policy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			originResult <- err
			return
		}
		defer conn.Close()
		got, err := wsReadTestMessage(buffer)
		if err != nil || string(got) != request {
			originResult <- errors.New("request changed before model origin")
			return
		}
		if _, err = conn.Write(wsTestWire(1, true, false, []byte(created))); err != nil {
			originResult <- err
			return
		}
		if _, err = conn.Write(wsTestWire(1, true, false, []byte(terminal))); err != nil {
			originResult <- err
			return
		}
		close(readAhead)
		var probe [1]byte
		if _, err = buffer.Read(probe[:]); err == nil {
			originResult <- errors.New("extra request reached origin after budget")
			return
		}
		originResult <- nil
	}))
	guest, reader := openModelWebSocket(t, fixture)
	if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
		t.Fatal(err)
	}
	if got, err := wsReadTestMessage(reader); err != nil || string(got) != created {
		t.Fatal("created payload changed", err)
	}
	select {
	case <-readAhead:
	case <-time.After(5 * time.Second):
		t.Fatal("actual origin did not finish its terminal write")
	}
	if got := fixture.env.srv.modelUsageSnapshot(); got.Total.Requests != 0 || got.Total.TotalTokens != 0 {
		t.Fatal("origin read-ahead counted before successful guest payload write")
	}
	if got, err := wsReadTestMessage(reader); err != nil || string(got) != terminal {
		t.Fatal("current response was cut or changed by budget close", err)
	}
	frame, err := wsReadTestFrame(reader)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload[:2]) != 1008 {
		t.Fatal("actual budget did not emit safe1008 after current response", err)
	}
	usage := fixture.env.srv.modelUsageSnapshot()
	if usage.Total.Requests != 1 || usage.Total.TotalTokens != 7 || usage.Total.EstimatedRequests != 0 {
		t.Fatal("logical terminal usage not counted exactly once")
	}
	select {
	case err := <-originResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("budget close did not physically end original origin")
	}
	before := fixture.requests.Load()
	refused, _ := fixture.request(t, "POST", "api.openai.com", "/v1/responses", []byte(`{"model":"model-a","input":[]}`), "")
	if refused.Code != 429 || refused.Header().Get("X-Should-Retry") != "false" || refused.Header().Get("Retry-After") != "" || fixture.requests.Load() != before {
		t.Fatal("next request retried or reached origin past current budget")
	}
}

func TestActualWebSocketUsageSeparatesInterleavedIDsAndIgnoresUnknownOrDuplicateTerminals(t *testing.T) {
	requests := []string{`{"type":"response.create","event_id":"one","model":"model-a","input":[]}`, `{"type":"response.create","event_id":"two","model":"model-b","input":[]}`}
	messages := []string{`{"type":"response.created","response":{"id":"response-a"}}`, `{"type":"response.created","response":{"id":"response-b"}}`, `{"type":"response.output_text.delta","response_id":"response-a","delta":"kept"}`, `{"type":"response.completed","response":{"id":"unknown","usage":{"total_tokens":999}}}`, `{"type":"response.completed","response":{"id":"response-b","usage":{"total_tokens":3}}}`, `{"type":"response.completed","response":{"id":"response-b","usage":{"total_tokens":999}}}`, `{"type":"response.completed","response":{"id":"response-a","usage":{"total_tokens":5}}}`}
	result := make(chan error, 1)
	fixture := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		for _, request := range requests {
			got, err := wsReadTestMessage(buffer)
			if err != nil || string(got) != request {
				result <- errors.New("concurrent request changed")
				return
			}
		}
		for _, message := range messages {
			if _, err := conn.Write(wsTestWire(1, true, false, []byte(message))); err != nil {
				result <- err
				return
			}
		}
		_, err = conn.Write(wsTestWire(webSocketClose, true, false, []byte{3, 232}))
		result <- err
	}))
	guest, reader := openModelWebSocket(t, fixture)
	for _, request := range requests {
		if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
			t.Fatal(err)
		}
	}
	for _, message := range messages {
		got, err := wsReadTestMessage(reader)
		if err != nil || !bytes.Equal(got, []byte(message)) {
			t.Fatal("interleaved response bytes changed", err)
		}
	}
	frame, err := wsReadTestFrame(reader)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload) != 1000 {
		t.Fatal("valid interleaved stream was closed by counter", err)
	}
	usage := fixture.env.srv.modelUsageSnapshot()
	if usage.Total.Requests != 2 || usage.Total.TotalTokens != 8 || usage.Total.EstimatedRequests != 0 || len(usage.Models) != 2 {
		t.Fatal("unknown or duplicate terminal consumed another response or counted extra tokens")
	}
	counts := map[string]uint64{}
	for _, model := range usage.Models {
		counts[model.Model] = model.Counts.TotalTokens
	}
	if counts["model-a"] != 5 || counts["model-b"] != 3 {
		t.Fatal("out-of-order terminals were attributed to wrong logical request")
	}
	if err := <-result; err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
