package supervisor

import (
	"encoding/json"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The sandbox may exit just after its final event reached the ctl reader.
// The final record must wait for that reader to apply the event.
func TestSystemStopDrainsTheControlReadersFinalClaim(t *testing.T) {
	started := make(chan *Supervisor, 1)
	old := testStarted
	testStarted = func(s *Supervisor) { started <- s }
	t.Cleanup(func() { testStarted = old })
	signals := fakeSignals(t)
	h := hostileStart(t)
	req := h.readReq(t)
	res, _ := json.Marshal(proto.CtlStartResult{AgentPID: 7, GuestABI: proto.GuestPTY1000V1})
	if err := h.w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res}); err != nil {
		t.Fatal(err)
	}
	s := <-started
	sig := signals()
	sig <- syscall.SIGTERM
	stop := h.readReq(t)
	if stop.Op != proto.CtlStop {
		t.Fatalf("stop op=%s", stop.Op)
	}
	if err := h.w.WriteJSON(proto.TResponse, proto.Response{ID: stop.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stop request's acknowledgement", func() bool {
		s.ctl.mu.Lock()
		_, pending := s.ctl.waits[stop.ID]
		s.ctl.mu.Unlock()
		return !pending
	})

	s.evMu.Lock()
	var unlock sync.Once
	t.Cleanup(func() { unlock.Do(s.evMu.Unlock) })
	s.ctl.mu.Lock()
	before := s.ctl.rate.last
	s.ctl.mu.Unlock()
	code := 7
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code})
	waitFor(t, "the final event to enter the ctl reader", func() bool {
		s.ctl.mu.Lock()
		passed := s.ctl.rate.last.After(before)
		s.ctl.mu.Unlock()
		return passed
	})
	if err := h.g.Kill(); err != nil {
		t.Fatal(err)
	}
	// Hold only event application, after the stop reply was consumed.
	// A final save here would permanently lose an already received claim.
	select {
	case <-h.ended:
		t.Fatal("supervisor finalised while its ctl reader held the final claim")
	case <-time.After(100 * time.Millisecond):
	}
	unlock.Do(s.evMu.Unlock)
	m := lastRecord(t, h)
	if m.Life.End == nil || m.Life.End.Kind != host.EndSystem || !m.Life.End.Claimed ||
		!m.Status.Claimed || m.Status.ExitCode == nil || *m.Status.ExitCode != code || !m.Ended() {
		t.Fatalf("system stop lost the final claim: life=%+v status=%+v guest=%+v", m.Life, m.Status, m.Guest)
	}
}
