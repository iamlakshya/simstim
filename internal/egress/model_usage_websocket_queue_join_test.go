package egress

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestActualWebSocketFullAccountingQueueJoinsOnOriginFailure(t *testing.T) {
	var requests []string
	for index := 0; index <= maxActiveFlows; index++ {
		requests = append(requests, fmt.Sprintf(`{"type":"response.create","event_id":"request-%d","model":"model-a","input":[]}`, index))
	}
	endOrigin := make(chan struct{})
	received := make(chan error, 1)
	fixture := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			received <- err
			return
		}
		defer conn.Close()
		for _, request := range requests[:maxActiveFlows] {
			got, err := wsReadTestMessage(buffer)
			if err != nil || string(got) != request {
				received <- fmt.Errorf("full accounting fixture request changed: %w", err)
				return
			}
		}
		received <- nil
		select {
		case <-endOrigin:
		case <-t.Context().Done():
		}
	}))
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
		t.Fatal("checked origin dial lost its original request")
	}
	for _, request := range requests[:maxActiveFlows] {
		if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
	attempted := make(chan struct{})
	go func() {
		_, _ = guest.Write(wsTestWire(1, true, true, []byte(requests[maxActiveFlows])))
		close(attempted)
	}()
	lastID := sha256.Sum256([]byte(fmt.Sprintf("request-%d", maxActiveFlows)))
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		scope.mu.Lock()
		model, usage := scope.model, scope.usageWS
		installed := model != nil && model.hasUsageEventID && model.usageEventID == lastID
		scope.mu.Unlock()
		if installed {
			usage.state.mu.Lock()
			active, reserved := len(usage.state.records), usage.state.reserved
			usage.state.mu.Unlock()
			if active != maxActiveFlows || reserved != 0 {
				t.Fatal("accounting exceeded its original128 active work bound")
			}
			break
		}
		select {
		case <-scope.ctx.Done():
			t.Fatal("accounting cut the original socket before the test origin failed")
		case <-deadline.C:
			t.Fatal("full accounting queue did not inspect its next logical request")
		case <-tick.C:
		}
	}
	// Keep the guest open: only the bound worker's original cancellation can
	// release the full reserve and let bound.Close physically join its jobs.
	close(endOrigin)
	frame, err := wsReadTestFrame(reader)
	if err != nil || frame.opcode != webSocketClose || len(frame.payload) < 2 || binary.BigEndian.Uint16(frame.payload[:2]) != 1002 {
		t.Fatal("origin failure did not finish its original close frame", err)
	}
	usage := waitActualUsageRequests(t, fixture.env.srv, maxActiveFlows)
	var estimate uint64
	for _, request := range requests[:maxActiveFlows] {
		count, _ := modelUsageEstimateV1(uint64(len(request)), 0)
		estimate += count
	}
	if usage.Total.EstimatedRequests != maxActiveFlows || usage.Total.TotalTokens != estimate || !api.ValidModelUsageV1(usage) {
		t.Fatal("origin failure lost queued requests or counted the blocked129th request")
	}
	select {
	case <-attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("origin failure retained its original guest write")
	}
}
