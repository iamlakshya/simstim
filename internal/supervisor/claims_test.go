package supervisor

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/proto"
)

const testSession = "abcd2345"

func event(t testing.TB, ev proto.CtlEvent) []byte {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Each rule of spec 10.3 for an event: the session, the states, the source's
// form, the alert kinds, the agent's ID form and the exit code.
func TestParseEvent(t *testing.T) {
	code, bad := 3, 300
	for _, c := range []struct {
		name string
		ev   proto.CtlEvent
		ok   bool
	}{
		{"heartbeat", proto.CtlEvent{Event: proto.EventHeartbeat}, true},
		{"status", proto.CtlEvent{Event: "status", Session: testSession, State: "working", Source: "screen:claude.busy"}, true},
		{"another session", proto.CtlEvent{Event: "status", Session: "zzzz2222", State: "working", Source: "output"}, false},
		{"no session", proto.CtlEvent{Event: "status", State: "working", Source: "output"}, false},
		{"a trusted state", proto.CtlEvent{Event: "status", Session: testSession, State: "exited", Source: "output"}, false},
		{"starting", proto.CtlEvent{Event: "status", Session: testSession, State: "starting", Source: "output"}, false},
		{"a source with a space", proto.CtlEvent{Event: "status", Session: testSession, State: "idle", Source: "a b"}, false},
		{"an empty source", proto.CtlEvent{Event: "status", Session: testSession, State: "idle"}, false},
		{"a long source", proto.CtlEvent{Event: "status", Session: testSession, State: "idle", Source: strings.Repeat("a", 65)}, false},
		{"needs_input", proto.CtlEvent{Event: "alert", Session: testSession, Kind: "needs_input", Detail: "Proceed?"}, true},
		{"turn_done", proto.CtlEvent{Event: "alert", Session: testSession, Kind: "turn_done"}, true},
		{"a memory alert", proto.CtlEvent{Event: "alert", Session: testSession, Kind: "memory"}, false},
		{"an oom alert", proto.CtlEvent{Event: "alert", Session: testSession, Kind: "oom"}, false},
		{"a UUID", proto.CtlEvent{Event: "agent_id", Session: testSession, ID: "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"}, true},
		{"a flag as an ID", proto.CtlEvent{Event: "agent_id", Session: testSession, ID: "--dangerously-skip-permissions"}, false},
		{"ended", proto.CtlEvent{Event: "ended", Session: testSession, ExitCode: &code, Signal: "killed"}, true},
		{"ended without a code", proto.CtlEvent{Event: "ended", Session: testSession}, false},
		{"an exit code above 255", proto.CtlEvent{Event: "ended", Session: testSession, ExitCode: &bad}, false},
		{"an unknown event", proto.CtlEvent{Event: "ports", Session: testSession}, false},
	} {
		_, ok := parseEvent(event(t, c.ev), testSession, "claude")
		if ok != c.ok {
			t.Errorf("%s: kept = %v, want %v", c.name, ok, c.ok)
		}
	}
	if _, ok := parseEvent([]byte("not json"), testSession, "claude"); ok {
		t.Error("an event that is not JSON was kept")
	}
	c, _ := parseEvent(event(t, proto.CtlEvent{Event: "status", Session: testSession, State: "needs_input", Source: "notify",
		Reason: "\x1b[31mred\x1b[0m\n" + strings.Repeat("é", 300)}), testSession, "claude")
	if strings.ContainsAny(c.reason, "\x1b\n") || utf8.RuneCountInString(c.reason) != maxReason {
		t.Errorf("reason = %q, want it cleaned and cut to %d runes", c.reason, maxReason)
	}
}

// The rate: 20 events at once pass, the 21st does not, and the bucket
// fills again at 20 a second.
func TestEventRate(t *testing.T) {
	var l rateLimit
	now := time.Now()
	for i := range eventRate {
		if !l.allow(now) {
			t.Fatalf("event %d of a burst was dropped", i+1)
		}
	}
	if l.allow(now) {
		t.Fatal("an event over the rate passed")
	}
	if !l.allow(now.Add(60 * time.Millisecond)) {
		t.Fatal("the bucket did not fill again")
	}
	if l.allow(now.Add(60 * time.Millisecond)) {
		t.Fatal("the bucket filled more than the time allows")
	}
}

// A frame over the cap is refused before its body is read.
func TestReadCappedRefusesALargeFrameFirst(t *testing.T) {
	var head [5]byte
	binary.LittleEndian.PutUint32(head[:4], maxEvent+1)
	head[4] = byte(proto.TEvent)
	_, err := readCapped(bufio.NewReader(bytes.NewReader(head[:])))
	if !errors.Is(err, errFrame) {
		t.Fatalf("a frame over the cap = %v, want it refused", err)
	}
	var b bytes.Buffer
	_ = proto.NewWriter(&b).Write(proto.TEvent, []byte("{}"))
	f, err := readCapped(bufio.NewReader(&b))
	if err != nil || f.Type != proto.TEvent || string(f.Payload) != "{}" {
		t.Fatalf("a small frame = %v %q, %v", f.Type, f.Payload, err)
	}
	cut := []byte{10, 0, 0, 0, byte(proto.TEvent), '{'}
	if _, err := readCapped(bufio.NewReader(bytes.NewReader(cut))); !errors.Is(err, errFrame) {
		t.Fatalf("a frame cut short = %v, want a breach", err)
	}
}

// A turn's time counts each working spell, and turn_done takes it.
func TestTurnClock(t *testing.T) {
	var c turnClock
	now := time.Now()
	c.since = now
	c.change("working", "needs_input", now.Add(2*time.Second))
	c.change("needs_input", "working", now.Add(5*time.Second))
	if d := c.take(now.Add(6 * time.Second)); d != 3*time.Second {
		t.Errorf("turn = %v, want 3s", d)
	}
	// The guest sends the status that ends the turn, then turn_done.
	c.change("idle", "working", now.Add(10*time.Second))
	c.change("working", "idle", now.Add(14*time.Second))
	if d := c.take(now.Add(20 * time.Second)); d != 4*time.Second {
		t.Errorf("the ended turn = %v, want 4s", d)
	}
	if d := c.take(now.Add(21 * time.Second)); d != 0 {
		t.Errorf("after the turn = %v, want 0", d)
	}
}

// FuzzCtlEvent feeds the ctl stream's reader and the event parser with any
// bytes. Neither panics, and every string that a kept claim holds is clean
// and within its cap.
func FuzzCtlEvent(f *testing.F) {
	code := 1
	for _, ev := range []proto.CtlEvent{
		{Event: "status", Session: testSession, State: "working", Source: "output", Reason: "x"},
		{Event: "alert", Session: testSession, Kind: "needs_input", Detail: "Proceed?\x1b]52;c;aGk=\x07"},
		{Event: "agent_id", Session: testSession, ID: "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"},
		{Event: "ended", Session: testSession, ExitCode: &code, Signal: "killed"},
		{Event: "heartbeat"},
	} {
		var b bytes.Buffer
		_ = proto.NewWriter(&b).WriteJSON(proto.TEvent, ev)
		f.Add(b.Bytes())
	}
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0x30})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bufio.NewReader(bytes.NewReader(data))
		for {
			fr, err := readCapped(r)
			if err != nil {
				return
			}
			if len(fr.Payload)+1 > maxEvent {
				t.Fatalf("a frame of %d bytes passed the cap", len(fr.Payload)+1)
			}
			c, ok := parseEvent(fr.Payload, testSession, "claude")
			if !ok {
				continue
			}
			for _, s := range []struct {
				v   string
				max int
			}{{c.reason, maxReason}, {c.detail, maxDetail}, {c.signal, maxSignal}, {c.source, 64}, {c.id, 64}} {
				if utf8.RuneCountInString(s.v) > s.max || strings.IndexFunc(s.v, unicode.IsControl) >= 0 {
					t.Fatalf("a kept claim holds %q", s.v)
				}
			}
		}
	})
}
