package egress

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type holdAuthoredEntropy struct{ next uint64 }

func (r *holdAuthoredEntropy) Read(p []byte) (int, error) {
	if len(p) < 8 {
		return 0, io.ErrShortBuffer
	}
	clear(p)
	r.next++
	binary.BigEndian.PutUint64(p[len(p)-8:], r.next)
	return len(p), nil
}

func newHoldStateFixture(t *testing.T, scrubber *Scrubber) *networkHolds {
	t.Helper()
	holds, err := newNetworkHolds(scrubber)
	if err != nil {
		t.Fatal(err)
	}
	holds.entropy = new(holdAuthoredEntropy)
	t.Cleanup(holds.close)
	return holds
}

func TestNetworkHoldCapacityDeduplicatesWithoutEvictionOrTTLShortening(t *testing.T) {
	holds := newHoldStateFixture(t, NewScrubber(nil))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var first networkHoldTicket
	for i := range maxActiveFlows {
		host := fmt.Sprintf("authored%d.example", i)
		ticket, _, err := holds.attach(Request{Host: host, Port: 443}, host, now, 5*time.Minute)
		if err != nil {
			t.Fatal("existing flow-sized pending capacity was not available", err)
		}
		if i == 0 {
			first = ticket
		}
	}
	before := holds.snapshot()
	if len(before.Requests) != maxActiveFlows {
		t.Fatal("pending capacity does not reuse the existing active-flow ceiling")
	}
	for range 20 {
		if _, _, err := holds.attach(Request{Host: "overflow.example", Port: 443}, "overflow.example", now.Add(time.Minute), 5*time.Minute); !errors.Is(err, errHoldPressure) {
			t.Fatal("pressure did not refuse only creation of the new question")
		}
	}
	if len(holds.snapshot().Requests) != maxActiveFlows || holds.refusedCount(now) != 0 {
		t.Fatal("uncreated pressure requests evicted an old hold or invented refusals")
	}
	joined, _, err := holds.attach(Request{Host: "authored0.example", Port: 443}, "authored0.example", now.Add(4*time.Minute), 10*time.Minute)
	if err != nil || joined.entry != first.entry || !joined.entry.meta.Expires.Equal(now.Add(5*time.Minute)) {
		t.Fatal("joining a full table spent a record or extended its original full TTL", err)
	}
	holds.detach(first)
	holds.detach(joined)
	if len(holds.snapshot().Requests) != maxActiveFlows {
		t.Fatal("flow cancellation removed the retained question")
	}
	if changes := holds.expire(now.Add(5*time.Minute - time.Nanosecond)); len(changes) != 0 {
		t.Fatal("pressure or cancellation shortened an existing ask_open")
	}
	if changes := holds.expire(now.Add(5 * time.Minute)); len(changes) != maxActiveFlows || len(holds.snapshot().Requests) != 0 {
		t.Fatal("the actual full TTL did not retire every existing question exactly once")
	}
	if changes := holds.expire(now.Add(6 * time.Minute)); len(changes) != 0 {
		t.Fatal("an already-ended question was counted twice")
	}
}

func TestNetworkHoldRefusalCountsQuestionOnceAndRejectsLateIDs(t *testing.T) {
	holds := newHoldStateFixture(t, NewScrubber(nil))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var ticket networkHoldTicket
	for range 20 {
		current, _, err := holds.attach(Request{Host: "authored.example", Port: 443}, "authored.example", now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		ticket = current
	}
	id := ticket.entry.meta.ID
	if _, ok := holds.refuse(id, now.Add(time.Second)); !ok || holds.refusedCount(now.Add(time.Second)) != 1 {
		t.Fatal("twenty waiting flows did not count as one refused question")
	}
	if _, ok := holds.refuse(id, now.Add(2*time.Second)); ok {
		t.Fatal("late refusal revived an ended ID")
	}
	if _, ok := holds.answer(ticket, now.Add(2*time.Second)); ok || holds.refusedCount(now.Add(2*time.Second)) != 1 {
		t.Fatal("late answer gained meaning or duplicated the refusal")
	}
	if holds.refusedCount(now.Add(10*time.Minute+time.Second)) != 0 {
		t.Fatal("a refusal did not leave the exact ten-minute window")
	}
	newTicket, _, err := holds.attach(Request{Host: "authored.example", Port: 443}, "authored.example", now.Add(11*time.Minute), time.Minute)
	if err != nil || newTicket.entry.meta.ID == id {
		t.Fatal("a later real question reused a stale opaque ID", err)
	}
	holds.expire(now.Add(12 * time.Minute))
	if _, ok := holds.answer(newTicket, now.Add(12*time.Minute)); ok || holds.refusedCount(now.Add(12*time.Minute)) != 1 {
		t.Fatal("late answer bypassed expiry or counted per waiting flow")
	}
}

func TestNetworkHoldDisplayUsesLiveSourceRedactionAndPrivateCanonicalTarget(t *testing.T) {
	value := "AuthoredExposure"
	scrubber := NewScrubber(map[string]string{"EXPOSED": value})
	holds := newHoldStateFixture(t, scrubber)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	canonical := strings.ToLower(value) + ".example.test"
	ticket, _, err := holds.attach(Request{Host: canonical, Port: 443}, value+".example.test:443", now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := holds.snapshot()
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(snapshot.Requests) != 1 || !api.ValidPendingNetworkRequest(snapshot.Requests[0]) || !snapshot.Requests[0].Redacted || strings.Contains(string(encoded), value) || strings.Contains(string(encoded), strings.ToLower(value)) {
		t.Fatal("a source-known value reached pending public display", err)
	}
	if ticket.entry.request.Host != canonical || ticket.entry.request.Port != 443 || snapshot.Requests[0].Host == canonical {
		t.Fatal("masked display replaced the true private target or gained target meaning")
	}
	if _, _, err := holds.attach(Request{Host: snapshot.Requests[0].Host, Port: 443}, snapshot.Requests[0].Host, now, time.Minute); err == nil {
		t.Fatal("redacted display text was accepted as a canonical target")
	}
	lateHost := "newlyknownvalue.example.test"
	if _, _, err := holds.attach(Request{Host: lateHost, Port: 443}, lateHost, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	before := holds.currentRevision()
	scrubber.SetSecrets(map[string]string{"EXPOSED": value, "NEW": "newlyknownvalue"})
	holds.policyChanged(false) // same invalidation after actual source publication
	if holds.currentRevision() <= before {
		t.Fatal("new redaction knowledge did not invalidate a retained public view")
	}
	encoded, err = json.Marshal(holds.snapshot())
	if err != nil || strings.Contains(string(encoded), "newlyknownvalue") || strings.Contains(string(encoded), value) || strings.Contains(string(encoded), strings.ToLower(value)) {
		t.Fatal("retained questions reused a stale unredacted host view", err)
	}
}
