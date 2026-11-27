package egress

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestActualWebSocketBudgetAttemptWaitsForEveryAdmittedResponse(t *testing.T) {
	requests := []string{`{"type":"response.create","event_id":"one","model":"model-a","input":[]}`, `{"type":"response.create","event_id":"two","model":"model-b","input":[]}`, `{"type":"response.create","event_id":"three","model":"model-c","input":[]}`}
	createdA := `{"type":"response.created","response":{"id":"response-a"}}`
	createdB := `{"type":"response.created","response":{"id":"response-b"}}`
	terminalA := `{"type":"response.completed","response":{"id":"response-a","usage":{"total_tokens":7}}}`
	terminalB := `{"type":"response.completed","response":{"id":"response-b","usage":{"total_tokens":3}}}`
	releaseSecond := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseSecond) })
	defer release()
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
		for _, request := range requests[:2] {
			got, err := wsReadTestMessage(buffer)
			if err != nil || string(got) != request {
				originResult <- errors.New("admitted model request changed")
				return
			}
		}
		for _, message := range []string{createdA, createdB, terminalA} {
			if _, err := conn.Write(wsTestWire(1, true, false, []byte(message))); err != nil {
				originResult <- err
				return
			}
		}
		select {
		case <-releaseSecond:
		case <-t.Context().Done():
			originResult <- t.Context().Err()
			return
		}
		if _, err := conn.Write(wsTestWire(1, true, false, []byte(terminalB))); err != nil {
			originResult <- err
			return
		}
		var probe [1]byte
		if _, err := buffer.Read(probe[:]); err == nil {
			originResult <- errors.New("new model request reached origin after budget")
			return
		}
		originResult <- nil
	}))
	// Capture the actual original request from its existing checked dial.
	// This observes the live object; it manufactures no permission or scope.
	captured := make(chan *originRequest, 1)
	originalDial := fixture.env.srv.dialer.DialTCP
	fixture.env.srv.dialer.DialTCP = func(ctx context.Context, address netip.AddrPort) (net.Conn, error) {
		scope, _ := ctx.Value(originRequestKey{}).(*originRequest)
		captured <- scope
		return originalDial(ctx, address)
	}
	guest, reader := openModelWebSocket(t, fixture)
	scope := <-captured
	if scope == nil {
		t.Fatal("actual checked dial lost its original request")
	}
	for _, request := range requests[:2] {
		if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
			t.Fatal(err)
		}
	}
	for _, message := range []string{createdA, createdB, terminalA} {
		got, err := wsReadTestMessage(reader)
		if err != nil || string(got) != message {
			t.Fatal("admitted response changed", err)
		}
	}
	waitActualUsageRequests(t, fixture.env.srv, 1)
	attempted := make(chan struct{})
	go func() {
		_, _ = guest.Write(wsTestWire(1, true, true, []byte(requests[2])))
		close(attempted)
	}()
	// A positive marker proves that the actual logical request reached the
	// existing install path while the second response is still withheld.
	thirdID := sha256.Sum256([]byte("three"))
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		scope.mu.Lock()
		model := scope.model
		installed := model != nil && model.hasUsageEventID && model.usageEventID == thirdID
		usage := scope.usageWS
		scope.mu.Unlock()
		if installed {
			usage.state.mu.Lock()
			remaining, exhausted := len(usage.state.records), usage.state.exhausted
			usage.state.mu.Unlock()
			if remaining != 1 {
				t.Fatal("budget attempt changed an admitted response")
			}
			if exhausted {
				break
			}
		}
		select {
		case <-scope.ctx.Done():
			t.Fatal("budget attempt cut the still-admitted response")
		case <-deadline.C:
			t.Fatal("logical budget attempt did not reach its original live owner")
		case <-tick.C:
		}
	}
	release()
	got, err := wsReadTestMessage(reader)
	if err != nil || string(got) != terminalB {
		t.Fatal("budget close cut the remaining admitted terminal", err)
	}
	frame, err := wsReadTestFrame(reader)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload[:2]) != 1008 {
		t.Fatal("exhausted socket did not close1008 after both responses", err)
	}
	usage := waitActualUsageRequests(t, fixture.env.srv, 2)
	if usage.Total.TotalTokens != 10 || usage.Total.EstimatedRequests != 0 || !api.ValidModelUsageV1(usage) {
		t.Fatal("quota attempt lost admitted usage or counted the refused third request")
	}
	select {
	case err := <-originResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("budget close did not physically end its original origin")
	}
	select {
	case <-attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("budget attempt retained its guest writer")
	}
}
