package supervisor

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// fakeSignals gives the test the signals of each supervisor that starts
// after it: the function returns the next supervisor's channel.
func fakeSignals(t *testing.T) func() chan<- os.Signal {
	t.Helper()
	ch := make(chan chan<- os.Signal, 4)
	oldN, oldS := notifySignals, stopSignals
	notifySignals = func(c chan<- os.Signal) { ch <- c }
	stopSignals = func(chan<- os.Signal) {}
	t.Cleanup(func() { notifySignals, stopSignals = oldN, oldS })
	return func() chan<- os.Signal {
		t.Helper()
		select {
		case c := <-ch:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("no supervisor took the signals")
		}
		return nil
	}
}

// lastRecord is the session's manifest after its supervisor ended.
func lastRecord(t *testing.T, h *hostileRun) *host.Manifest {
	t.Helper()
	_ = h.waitEnd(t, 20*time.Second)
	m, err := host.LoadManifest(h.id)
	if err != nil || m.Life == nil {
		t.Fatalf("no record: %+v, %v", m, err)
	}
	return m
}

// A stop with the host after the guest claimed the agent's end records the
// trusted end, a stop with the host, which keeps the claim: the row shows
// the claimed end, marked as claimed, and restore leaves the session, as
// it leaves one whose agent ended without a wall. A forged end gives the
// guest nothing that its own exit would not.
func TestAStopWithTheHostKeepsAClaimedEndAsClaimed(t *testing.T) {
	signals := fakeSignals(t)
	h := hostileStart(t)
	sig := signals()
	h.answerStart(t)
	zero := 0
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &zero})
	waitFor(t, "the claimed end", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Status.ExitCode != nil
	})
	sig <- syscall.SIGTERM
	m := lastRecord(t, h)
	if m.Life.End == nil || m.Life.End.Kind != host.EndSystem || !m.Life.End.Claimed || !m.Ended() {
		t.Errorf("the record after a stop with the host: %+v", m.Life.End)
	}
	if m.Status.ExitCode == nil || m.Status.State != host.StateExited || !m.Status.Claimed {
		t.Errorf("the last status does not show the claimed end as claimed: %+v", m.Status)
	}
	if m.Guest == nil || m.Guest.ExitCode == nil {
		t.Errorf("the claim is gone: %+v", m.Guest)
	}
}

// A kill at the cap during the boot counts: the watch begins before the
// guest starts.
func TestAKillDuringTheBootCounts(t *testing.T) {
	set := fakeCgroup(t)
	h := hostileStart(t)
	req := h.readReq(t)
	set("memory.events", "max 3\noom 1\noom_kill 1")
	res, _ := json.Marshal(proto.CtlStartResult{AgentPID: 7, GuestABI: proto.GuestPTY1000V1})
	_ = h.w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res})
	waitFor(t, "the kill in the boot", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Status.OOM && m.Status.Reason == host.OOMChildReason
	})
	if b, _ := os.ReadFile(paths.AuditLog(h.id)); !strings.Contains(string(b), `"event":"oom_kill"`) {
		t.Errorf("no audit line of the kill:\n%s", b)
	}
}

// A kill at the cap that no tick took before the sandbox ends is still a
// fact of the run: its mark, its audit line and its alert.
func TestAKillAtTheEndIsNeverLost(t *testing.T) {
	set := fakeCgroup(t)
	h := hostileStart(t)
	got := alertsTo(t)
	h.answerStart(t)
	set("memory.events", "max 3\noom 1\noom_kill 1")
	_ = h.g.Kill()
	m := lastRecord(t, h)
	if m.Life.End == nil || !m.Life.End.OOM {
		t.Errorf("the end lost the kill: %+v %+v", m.Status, m.Life.End)
	}
	if b, _ := os.ReadFile(paths.AuditLog(h.id)); !strings.Contains(string(b), `"event":"oom_kill"`) {
		t.Errorf("no audit line of the kill:\n%s", b)
	}
	waitAlerts(t, got, alerts.OOM)
}

// A kill that a tick took for another process is never the agent's: an
// end frame with a claimed SIGKILL after it says no out-of-memory, as the
// row does.
func TestAnOldChildKillIsNotTheAgents(t *testing.T) {
	set := fakeCgroup(t)
	h := hostileStart(t)
	h.answerStart(t)
	set("memory.events", "max 3\noom 1\noom_kill 1")
	waitFor(t, "a child's kill", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Status.Reason == host.OOMChildReason
	})
	_, r, w := dial(t, h.id)
	_ = w.WriteJSON(proto.TAttach, proto.Attach{Session: h.id, Cols: 80, Rows: 24, LastOffset: -1})
	st := <-h.g.holders
	gr, gw := bufio.NewReader(st), proto.NewWriter(st)
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.TEnded, proto.Ended{ExitCode: 137, Signal: "killed"})
	f, err := proto.Read(r)
	var e proto.Ended
	if err != nil || f.Type != proto.TEnded || json.Unmarshal(f.Payload, &e) != nil {
		t.Fatalf("the client got %v %s, %v", f.Type, f.Payload, err)
	}
	if e.OOM {
		t.Errorf("the end frame says out of memory for a child's old kill: %+v", e)
	}
}

// An end that parseEvent drops never takes the room that the rate keeps
// for the agent's first end: the valid end after it still counts.
func TestAMalformedEndTakesNoRoom(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	for range 3 * eventRate {
		h.send(t, proto.CtlEvent{Event: proto.EventStatus, Session: h.id, State: host.StateIdle, Source: "screen"})
	}
	_ = h.w.Write(proto.TEvent, []byte(`{"event":"ended"}`))
	code := 2
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code})
	waitFor(t, "the valid end", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Guest != nil && m.Guest.ExitCode != nil && *m.Guest.ExitCode == 2
	})
}

// The agent's first end has room of its own among the events before the
// answer to start, even when they fill their bound.
func TestTheFirstEndHasRoomBeforeTheStart(t *testing.T) {
	h := hostileStart(t)
	req := h.readReq(t)
	// Four events of 65530 bytes: the next 70 bytes are over the bound.
	head, tail := `{"event":"status","session":"`+h.id+`","state":"idle","source":"screen","reason":"`, `"}`
	big := head + strings.Repeat("x", maxEvent-6-len(head)-len(tail)) + tail
	for range 4 {
		_ = h.w.Write(proto.TEvent, []byte(big))
	}
	code := 3
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code})
	res, _ := json.Marshal(proto.CtlStartResult{AgentPID: 7, GuestABI: proto.GuestPTY1000V1})
	_ = h.w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res})
	waitFor(t, "the end from before the start", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Guest != nil && m.Guest.ExitCode != nil && *m.Guest.ExitCode == 3
	})
}

// Only a heartbeat says that the guest init is alive: ordinary and
// malformed events, however many, never hide a silence.
func TestOtherEventsDoNotHideASilence(t *testing.T) {
	var skew atomic.Int64
	old := now
	now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	t.Cleanup(func() { now = old })
	id, g, _ := supervised(t)
	got := alertsTo(t)
	done, stopped := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		close(done)
		<-stopped
	})
	skew.Store(int64(linkSilent + time.Second))
	go func() {
		defer close(stopped)
		for i := 0; ; i++ {
			g.mu.Lock()
			w := g.ctl
			g.mu.Unlock()
			if i%2 == 0 {
				_ = w.WriteJSON(proto.TEvent, proto.CtlEvent{Event: proto.EventStatus, Session: id, State: host.StateWorking, Source: "output"})
			} else {
				_ = w.Write(proto.TEvent, []byte(`{"event":"heartbeat"`))
			}
			select {
			case <-done:
				return
			case <-time.After(30 * time.Millisecond):
			}
		}
	}()
	waitAlerts(t, got, alerts.LinkSilent)
}
