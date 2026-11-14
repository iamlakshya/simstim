package supervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// alertEventsTo is alertsTo with the whole events.
func alertEventsTo(t *testing.T) <-chan alerts.Event {
	t.Helper()
	got := make(chan alerts.Event, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ev alerts.Event
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &ev) == nil {
			got <- ev
		}
	}))
	t.Cleanup(srv.Close)
	if err := os.MkdirAll(filepath.Dir(paths.AlertsConfig()), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "events = [\"exited\", \"failed\", \"oom\"]\n[[destination]]\ntype = \"webhook\"\nurl = \"" + srv.URL + "\"\n"
	if err := os.WriteFile(paths.AlertsConfig(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return got
}

// endFrame attaches, and sends the end frame e as the holder.
func endFrame(t *testing.T, h *hostileRun, e proto.Ended) proto.Ended {
	t.Helper()
	_, r, w := dial(t, h.id)
	_ = w.WriteJSON(proto.TAttach, proto.Attach{Session: h.id, Cols: 80, Rows: 24, LastOffset: -1})
	st := <-h.g.holders
	gr, gw := bufio.NewReader(st), proto.NewWriter(st)
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.TEnded, e)
	f, err := proto.Read(r)
	var got proto.Ended
	if err != nil || f.Type != proto.TEnded || json.Unmarshal(f.Payload, &got) != nil {
		t.Fatalf("the client got %v %s, %v", f.Type, f.Payload, err)
	}
	return got
}

// The agent's kill at the cap raises its oom alert when its end frame comes
// first, with no claimed end on the ctl stream: once, as the trusted side's
// fact, with the exit details marked as the guest's.
func TestAnEndFrameFirstRaisesTheAgentsOOM(t *testing.T) {
	set := fakeCgroup(t)
	h := hostileStart(t)
	got := alertEventsTo(t)
	h.answerStart(t)
	_, _, _ = dial(t, h.id) // the socket answers
	set("memory.events", "max 3\noom 1\noom_kill 1")
	if e := endFrame(t, h, proto.Ended{ExitCode: 137, Signal: "killed"}); !e.OOM {
		t.Fatalf("the end frame after a kill at the cap = %+v", e)
	}
	select {
	case ev := <-got:
		if ev.Event != alerts.OOM || ev.Claimed || ev.ExitCode == nil || ev.ExitFrom != alerts.FromGuest {
			t.Errorf("the agent's oom alert = %+v", ev)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no oom alert")
	}
	code := 137
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code, Signal: "killed"})
	select {
	case ev := <-got:
		t.Errorf("a second alert for the same end: %+v", ev)
	case <-time.After(2 * time.Second):
	}
}

// The classification of the agent's end is made once: an end frame
// without SIGKILL decides it, and a kill that comes before the claimed end
// with SIGKILL does not change it.
func TestTheEndIsClassifiedOnce(t *testing.T) {
	set := fakeCgroup(t)
	h := hostileStart(t)
	h.answerStart(t)
	if e := endFrame(t, h, proto.Ended{ExitCode: 0}); e.OOM {
		t.Fatalf("an end frame with no kill = %+v", e)
	}
	set("memory.events", "max 3\noom 1\noom_kill 1")
	code := 137
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code, Signal: "killed"})
	waitFor(t, "the claimed end", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Status.ExitCode != nil
	})
	if _, err := host.HolderRequest(h.id, "kill", nil, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if m := lastRecord(t, h); m.Life.End == nil || m.Life.End.OOMAgent {
		t.Errorf("a later kill changed the end's classification: %+v", m.Life.End)
	}
}

// A supervisor whose listen fails says what to try.
func TestAFailedListenSaysWhatToTry(t *testing.T) {
	testFolders(t)
	// A runtime folder so deep that the socket's path is too long.
	deep := filepath.Join(os.TempDir(), strings.Repeat("d", 100))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(deep) })
	t.Setenv("XDG_RUNTIME_DIR", deep)
	s := &Supervisor{id: "abcd2345"}
	err := s.listen()
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "listen" || pe.Hint == "" || !strings.Contains(pe.Message, "listen on") {
		t.Fatalf("a listen that fails = %#v", err)
	}
}
