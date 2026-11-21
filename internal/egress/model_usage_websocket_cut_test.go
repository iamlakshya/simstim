package egress

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func waitActualUsageRequests(t *testing.T, server *Server, count uint64) api.ModelUsageV1 {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		usage := server.modelUsageSnapshot()
		if usage.Total.Requests == count {
			return usage
		}
		select {
		case <-deadline.C:
			t.Fatal("owned model responses did not finish after cancellation")
			return usage
		case <-tick.C:
		}
	}
}

func TestActualWebSocketCancellationEstimatesEveryAdmittedResponseWithoutFrameBytes(t *testing.T) {
	requests := []string{`{"type":"response.create","event_id":"one","model":"model-a","input":[]}`, `{"type":"response.create","event_id":"two","model":"model-b","input":[]}`}
	createdA := `{"type":"response.created","response":{"id":"response-a"}}`
	createdB := `{"type":"response.created","response":{"id":"response-b"}}`
	deltaA := `{"type":"response.output_text.delta","response_id":"response-a","delta":"kept"}`
	closed := make(chan struct{})
	fixture := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			t.Error(err)
			close(closed)
			return
		}
		defer conn.Close()
		defer close(closed)
		for _, request := range requests {
			got, err := wsReadTestMessage(buffer)
			if err != nil || string(got) != request {
				t.Error("cancel fixture request changed")
				return
			}
		}
		for _, message := range []string{createdA, createdB, deltaA} {
			if _, err := conn.Write(wsTestWire(1, true, false, []byte(message))); err != nil {
				t.Error(err)
				return
			}
		}
		if _, err := conn.Write(wsTestWire(webSocketPing, true, false, []byte("barrier"))); err != nil {
			t.Error(err)
			return
		}
		var probe [1]byte
		_, _ = buffer.Read(probe[:])
	}))
	guest, reader := openModelWebSocket(t, fixture)
	for _, request := range requests {
		if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
			t.Fatal(err)
		}
	}
	for _, message := range []string{createdA, createdB, deltaA} {
		got, err := wsReadTestMessage(reader)
		if err != nil || string(got) != message {
			t.Fatal("cancel fixture response changed", err)
		}
	}
	ping, err := wsReadTestFrame(reader)
	if err != nil || ping.opcode != webSocketPing {
		t.Fatal("actual successful-write barrier missing", err)
	}
	_ = guest.Close()
	usage := waitActualUsageRequests(t, fixture.env.srv, 2)
	first, _ := modelUsageEstimateV1(uint64(len(requests[0])), uint64(len(createdA)+len(deltaA)))
	second, _ := modelUsageEstimateV1(uint64(len(requests[1])), uint64(len(createdB)))
	if usage.Total.EstimatedRequests != 2 || usage.Total.TotalTokens != first+second {
		t.Fatal("cancellation dropped an admitted response, promoted partial usage or included frame/control bytes")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not physically close original origin")
	}
}

func TestActualWebSocketProviderErrorUsesTheFullExactClientEventIdentity(t *testing.T) {
	prefix := strings.Repeat("e", 1024)
	requests := []string{fmt.Sprintf(`{"type":"response.create","event_id":"%sa","model":"model-a","input":[]}`, prefix), fmt.Sprintf(`{"type":"response.create","event_id":"%s\u0062","model":"model-b","input":[]}`, prefix)}
	created := `{"type":"response.created","response":{"id":"response-a"}}`
	failure := fmt.Sprintf(`{"type":"error","event_id":"server-event","error":{"event_id":"%sb","message":"bad argument"}}`, prefix)
	terminal := `{"type":"response.completed","response":{"id":"response-a","usage":{"total_tokens":5}}}`
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
				result <- errors.New("long event identity request changed")
				return
			}
		}
		for _, message := range []string{created, failure, terminal} {
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
	for _, message := range []string{created, failure, terminal} {
		got, err := wsReadTestMessage(reader)
		if err != nil || string(got) != message {
			t.Fatal("provider error changed or closed another response", err)
		}
	}
	frame, err := wsReadTestFrame(reader)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload) != 1000 {
		t.Fatal("exact correlated provider error became a policy close", err)
	}
	usage := fixture.env.srv.modelUsageSnapshot()
	counts := map[string]api.ModelUsageCountsV1{}
	for _, model := range usage.Models {
		counts[model.Model] = model.Counts
	}
	estimate, _ := modelUsageEstimateV1(uint64(len(requests[1])), uint64(len(failure)))
	if usage.Total.Requests != 2 || usage.Total.EstimatedRequests != 1 || counts["model-a"].TotalTokens != 5 || counts["model-a"].EstimatedRequests != 0 || counts["model-b"].TotalTokens != estimate || counts["model-b"].EstimatedRequests != 1 {
		t.Fatal("truncated, escaped or server event ID selected the wrong model request")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
