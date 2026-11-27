package egress

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestActualWebSocketUsageAcceptsTheWholeEscapedProviderEventKind(t *testing.T) {
	var escaped strings.Builder
	for _, value := range "response.output_text.delta" {
		_, _ = fmt.Fprintf(&escaped, `\u%04x`, value)
	}
	if escaped.Len() <= 128 {
		t.Fatal("fixture no longer crosses the original raw encoded-kind limit")
	}
	const request = `{"type":"response.create","event_id":"first","model":"model-a","input":[]}`
	messages := []string{
		`{"type":"response.created","response":{"id":"response-a"}}`,
		`{"type":"` + escaped.String() + `","response_id":"response-a","delta":"kept"}`,
		`{"type":"` + strings.Repeat("unknown", 1024) + `","response_id":"response-a"}`,
		`{"type":"response.completed","response":{"id":"response-a","usage":{"total_tokens":7}}}`,
	}
	result := make(chan error, 1)
	fixture := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		got, err := wsReadTestMessage(buffer)
		if err != nil || string(got) != request {
			result <- errors.New("escaped provider kind request changed")
			return
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
	if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		got, err := wsReadTestMessage(reader)
		if err != nil || string(got) != message {
			t.Fatal("valid provider event spelling changed or became a policy close", err)
		}
	}
	frame, err := wsReadTestFrame(reader)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload[:2]) != 1000 {
		t.Fatal("escaped or unknown event kind became a policy failure", err)
	}
	usage := waitActualUsageRequests(t, fixture.env.srv, 1)
	if usage.Total.TotalTokens != 7 || usage.Total.EstimatedRequests != 0 || !api.ValidModelUsageV1(usage) {
		t.Fatal("escaped kind changed terminal usage or counted another request")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
