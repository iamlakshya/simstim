package egress

import (
	"encoding/binary"
	"errors"
	"net/http"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestActualWebSocketUsageFollowsEchoedLanesBeforeGlobalAdmissionOrder(t *testing.T) {
	requests := []string{
		`{"type":"response.create","event_id":"first","stream_id":"planner","model":"model-a","input":[]}`,
		`{"type":"response.create","event_id":"second","stream_id":"research","model":"model-b","input":[]}`,
		`{"type":"response.create","event_id":"third","stream_id":"planner","model":"model-c","input":[]}`,
		`{"type":"response.create","event_id":"fourth","model":"model-d","input":[]}`,
		`{"type":"response.create","event_id":"fifth","stream_id":"error-lane","model":"model-e","input":[]}`,
	}
	messages := []string{
		`{"type":"response.created","stream_id":"research","response":{"id":"response-b"}}`,
		`{"type":"response.output_text.delta","stream_id":"research","delta":"kept"}`,
		`{"type":"response.completed","stream_id":"research","response":{"id":"response-b","usage":{"total_tokens":3}}}`,
		`{"type":"response.created","stream_id":"planner","response":{"id":"response-a"}}`,
		`{"type":"response.completed","stream_id":"planner","response":{"id":"response-a","usage":{"total_tokens":5}}}`,
		`{"type":"response.created","stream_id":"planner","response":{"id":"response-c"}}`,
		`{"type":"response.completed","stream_id":"planner","response":{"id":"response-c","usage":{"total_tokens":7}}}`,
		`{"type":"response.created","response":{"id":"response-d"}}`,
		`{"type":"response.completed","response":{"id":"response-d","usage":{"total_tokens":11}}}`,
		`{"type":"error","stream_id":"error-lane","event_id":"first","status":400,"error":{"type":"invalid_request_error","message":"authored"}}`,
	}
	result := make(chan error, 1)
	fixture := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, reader, err := modelUpgradeOrigin(w, r)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		for _, request := range requests {
			got, err := wsReadTestMessage(reader)
			if err != nil || string(got) != request {
				result <- errors.New("lane request changed before the original origin")
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
		if err != nil || string(got) != message {
			t.Fatal("lane response bytes changed", err)
		}
	}
	frame, err := wsReadTestFrame(reader)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload) != 1000 {
		t.Fatal("valid named lanes became a policy close", err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	usage := waitActualUsageRequests(t, fixture.env.srv, uint64(len(requests)))
	counts := make(map[string]api.ModelUsageCountsV1)
	for _, row := range usage.Models {
		counts[row.Model] = row.Counts
	}
	for model, want := range map[string]uint64{"model-a": 5, "model-b": 3, "model-c": 7, "model-d": 11} {
		if counts[model].TotalTokens != want || counts[model].EstimatedRequests != 0 {
			t.Fatal("global FIFO or another lane selected the model", model, counts[model])
		}
	}
	estimate, _ := modelUsageEstimateV1(uint64(len(requests[4])), uint64(len(messages[9])))
	if counts["model-e"].Requests != 1 || counts["model-e"].EstimatedRequests != 1 || counts["model-e"].TotalTokens != estimate {
		t.Fatal("request-scoped lane error selected the outer provider event ID")
	}
}

func TestWebSocketLaneIdentityRecoveryKeepsEscapedWholeIdentity(t *testing.T) {
	model := &modelRequest{}
	if err := model.inspect([]byte(`{"type":"response.create","stream_id":"pl\u0061nner","model":"model-a"}`), true); err != nil {
		t.Fatal(err)
	}
	event, err := modelUsageWebSocketIdentityV1(t.Context(), []byte(`{"type":"response.completed","stream_id":"planner","response":{"id":"response-a","usage":{"total_tokens":"invalid"}}}`))
	if err != nil || !modelUsageSameLane(model, event) {
		t.Fatal("recovery lost the original decoded lane", err)
	}
	other, err := parseModelUsageEventV1([]byte(`{"type":"response.created","stream_id":"other","response":{"id":"response-b"}}`))
	if err != nil || modelUsageSameLane(model, other) {
		t.Fatal("another named lane aliased the original")
	}
}
