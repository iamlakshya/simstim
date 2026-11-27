package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
)

// Actual egress/control/supervisor snapshot route, with owned in-memory origin
// and link pipes. This is not native process/gVisor acceptance.
func TestHeldActualProducerRetainsZeroWaiterQuestionAndPrivateDisplay(t *testing.T) {
	value := "AuthoredExposure"
	fixture := newUnknownAcceptedFixture(t, map[string]string{"EXPOSED": value})
	wait, open := "5ms", "1m"
	policy, _, err := egress.MergeLayers([]api.Layer{{
		Kind:        api.LayerSession,
		Preset:      api.PresetLocked,
		Unknown:     api.UnknownAsk,
		LayerValues: api.LayerValues{Network: api.NetworkValues{Hold: &wait, AskOpen: &open}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := fixture.c.request(ctx, egress.CtlPolicyUpdate, egress.PolicyUpdateArgs{Policy: policy}, nil); err != nil {
		t.Fatal(err)
	}
	host := value + ".example.test"
	stream, err := fixture.s.eg.sess.Open(ctx, link.KindEgress, egress.StreamHeader{Via: "explicit", Host: host, Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(stream, "GET http://%s:80/authored HTTP/1.1\r\nHost: %s:80\r\nConnection: close\r\n\r\n", host, host); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stream)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusForbidden || strings.Contains(string(body), value) || strings.Contains(string(body), strings.ToLower(value)) {
		t.Fatal("actual held refusal exposed the private authority or reached origin", err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
	snapshot, failure := fixture.s.readNetworkHolds(ctx)
	if failure != nil || !api.ValidNetworkHoldSnapshot(snapshot) || len(snapshot.Requests) != 1 {
		t.Fatal("actual same-owner read lost the held question", failure)
	}
	row := snapshot.Requests[0]
	if row.Waiting != 0 || !row.Redacted || row.Expires.Sub(row.Opened) != time.Minute {
		t.Fatal("flow timeout removed, renewed or exposed the original question")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(encoded), value) || strings.Contains(string(encoded), strings.ToLower(value)) {
		t.Fatal("public snapshot exposed a credential through case-folded host", err)
	}
	var descriptor api.NetworkHoldDescriptor
	args := struct {
		Version int    `json:"v"`
		ID      string `json:"id"`
	}{api.NetworkHoldV1, row.ID}
	if err := fixture.c.request(ctx, egress.CtlNetworkDescribe, args, &descriptor); err != nil || !api.ValidNetworkHoldDescriptor(descriptor, row.ID) || descriptor.Request != row.ID || descriptor.Revision != row.Revision || !descriptor.Expires.Equal(row.Expires) {
		t.Fatal("opaque descriptor lost current private identity or expiry", err)
	}
	before := descriptor
	if err := fixture.c.request(ctx, egress.CtlNetworkDescribe, args, &descriptor); err != nil || descriptor != before {
		t.Fatal("read-only description renewed or changed its binding", err)
	}
	waitFor(t, "actual held refusal audit append", func() bool {
		data, err := os.ReadFile(fixture.auditPath)
		return err == nil && strings.Contains(string(data), `"kind":"net"`)
	})
	audit, err := os.ReadFile(fixture.auditPath)
	if err != nil || strings.Contains(string(audit), value) || strings.Contains(string(audit), strings.ToLower(value)) {
		t.Fatal("actual audit append exposed the source-known hostname value", err)
	}
	if err := fixture.c.request(ctx, egress.CtlNetworkRefuse, struct {
		ID string `json:"id"`
	}{row.ID}, nil); err != nil {
		t.Fatal(err)
	}
	after, failure := fixture.s.readNetworkHolds(ctx)
	if failure != nil || !api.ValidNetworkHoldSnapshot(after) || len(after.Requests) != 0 || after.Revision <= snapshot.Revision {
		t.Fatal("actual opaque refusal did not close exactly the current question", failure)
	}
	if err := fixture.c.request(ctx, egress.CtlNetworkDescribe, args, &descriptor); err == nil {
		t.Fatal("ended opaque ID regained descriptor meaning")
	}
}

func TestHeldSnapshotViewKeepsOpaqueCollisionsAndClearsOwner(t *testing.T) {
	opened := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := api.PendingNetworkRequest{Version: api.NetworkHoldV1, ID: strings.Repeat("a", 32), Revision: 2, Host: "[VALUE].example.test", Redacted: true, Port: 443, Opened: opened, Expires: opened.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	second := first
	second.ID = strings.Repeat("b", 32)
	snapshot := api.NetworkHoldSnapshot{Version: api.NetworkHoldV1, Revision: 2, Requests: []api.PendingNetworkRequest{first, second}}
	if !api.ValidNetworkHoldSnapshot(snapshot) {
		t.Fatal("authored complete snapshot is invalid")
	}
	view := new(networkHoldView)
	view.apply(snapshot)
	snapshot.Requests[0].Host = "caller-mutation"
	pending, _, reason := view.status()
	if !pending || strings.Contains(reason, "caller-mutation") || reason != "pending network request" {
		t.Fatal("bounded view aliased caller data or lost zero-waiter holds")
	}
	view.mu.Lock()
	rows := len(view.snapshot.Requests)
	view.mu.Unlock()
	if rows != 2 {
		t.Fatal("distinct opaque IDs sharing a masked Host were deduplicated")
	}
	view.apply(api.NetworkHoldSnapshot{Version: api.NetworkHoldV1, Revision: 1})
	if pending, _, _ := view.status(); !pending {
		t.Fatal("old snapshot erased a newer question")
	}
	view.clear()
	if pending, stopped, reason := view.status(); pending || stopped || reason != "" {
		t.Fatal("ended actor retained display authority")
	}
}
