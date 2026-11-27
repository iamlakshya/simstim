package egress

import (
	"encoding/binary"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestActualWebSocketProviderEventIDCannotSelectAClientRequest(t *testing.T) {
	requests := []string{`{"type":"response.create","event_id":"client-one","model":"model-a","input":[]}`, `{"type":"response.create","event_id":"client-two","model":"model-b","input":[]}`}
	// A server event can happen to have the same text as a client event. It
	// supplies no error.event_id with which to select either pending request.
	const failure = `{"type":"error","event_id":"client-two","error":{"message":"request failed"}}`
	closed := make(chan error, 1)
	fixture := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		for _, request := range requests {
			got, err := wsReadTestMessage(buffer)
			if err != nil || string(got) != request {
				closed <- errors.New("provider identity fixture request changed")
				return
			}
		}
		if _, err := conn.Write(wsTestWire(1, true, false, []byte(failure))); err != nil {
			closed <- err
			return
		}
		var probe [1]byte
		if _, err := buffer.Read(probe[:]); err == nil {
			closed <- errors.New("ambiguous provider error left the origin open")
			return
		}
		closed <- nil
	}))
	guest, reader := openModelWebSocket(t, fixture)
	for _, request := range requests {
		if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := wsReadTestMessage(reader)
	if err != nil || string(got) != failure {
		t.Fatal("provider error payload changed", err)
	}
	frame, err := wsReadTestFrame(reader)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload[:2]) != 1008 {
		t.Fatal("uncorrelated server identity retired a request instead of closing1008", err)
	}
	usage := waitActualUsageRequests(t, fixture.env.srv, 2)
	if usage.Total.EstimatedRequests != 2 || !api.ValidModelUsageV1(usage) {
		t.Fatal("ambiguous error promoted usage or dropped an admitted request")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("uncorrelated provider error did not physically close its origin")
	}
}
