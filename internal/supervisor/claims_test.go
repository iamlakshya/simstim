package supervisor

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
)

func TestParseEventValidationAndCleaning(t *testing.T) {
	t.Run("valid status is cleaned", func(t *testing.T) {
		raw := []byte(`{"type":"status","session":"abcd2345","state":"needs_input","source":"screen:claude.ask","reason":"Allow\u001b[31m\u202e\nnow","full_screen":true}`)
		ev, err := ParseEvent(raw)
		if err != nil {
			t.Fatalf("ParseEvent: %v", err)
		}
		if ev.State != api.StateNeedsInput || ev.Source != "screen:claude.ask" || !ev.FullScreen {
			t.Fatalf("unexpected event: %+v", ev)
		}
		if strings.ContainsAny(ev.Reason, "\x1b\n") || strings.ContainsRune(ev.Reason, '\u202e') {
			t.Fatalf("reason was not cleaned: %q", ev.Reason)
		}
	})

	t.Run("rejects forged end states in status", func(t *testing.T) {
		for _, bad := range []string{"exited", "errored", "lost", "starting", ""} {
			raw := []byte(`{"type":"status","state":"` + bad + `","source":"hook"}`)
			if _, err := ParseEvent(raw); err == nil {
				t.Errorf("ParseEvent accepted status state %q", bad)
			}
		}
	})

	t.Run("rejects bad source format", func(t *testing.T) {
		for _, bad := range []string{"BAD/SOURCE", "has space", strings.Repeat("a", 65)} {
			raw, _ := json.Marshal(guest.Event{Type: "status", State: api.StateWorking, Source: bad})
			if _, err := ParseEvent(raw); err == nil {
				t.Errorf("ParseEvent accepted source %q", bad)
			}
		}
	})

	t.Run("caps and cleans ports", func(t *testing.T) {
		ports := make([]api.Port, 80)
		for i := range ports {
			ports[i] = api.Port{Port: 3000 + i, Address: "127.0.0.1\x1b[0m", PID: i + 1, Process: "node\napp"}
		}
		ports[0].Port = 0 // invalid port dropped
		raw, _ := json.Marshal(guest.Event{Type: "ports", Ports: ports})
		ev, err := ParseEvent(raw)
		if err != nil {
			t.Fatalf("ParseEvent ports: %v", err)
		}
		if len(ev.Ports) != MaxClaimPorts-1 {
			t.Fatalf("got %d ports, want %d", len(ev.Ports), MaxClaimPorts-1)
		}
		if strings.Contains(ev.Ports[0].Address, "\x1b") || strings.Contains(ev.Ports[0].Process, "\n") {
			t.Fatalf("port fields not cleaned: %+v", ev.Ports[0])
		}
	})

	t.Run("caps payload size and rejects unknown types", func(t *testing.T) {
		if _, err := ParseEvent(nil); err == nil {
			t.Error("accepted empty event")
		}
		big := bytes.Repeat([]byte("a"), MaxCtlEventBytes+1)
		if _, err := ParseEvent(big); err == nil {
			t.Error("accepted oversized event")
		}
		if _, err := ParseEvent([]byte(`{"type":"bogus"}`)); err == nil {
			t.Error("accepted unknown type")
		}
		if _, err := ParseEvent([]byte(`{"type":"alert","kind":"exited"}`)); err == nil {
			t.Error("accepted forged exited alert")
		}
		if _, err := ParseEvent([]byte(`{"type":"alert","kind":"` + alerts.NeedsInput + `","detail":"ok"}`)); err != nil {
			t.Errorf("rejected valid alert: %v", err)
		}
	})
}

func TestEventLimiter(t *testing.T) {
	lim := newEventLimiter(MaxCtlEventsPerSec)
	now := time.Now()
	lim.last = now
	for i := range MaxCtlEventsPerSec {
		if !lim.Allow(now) {
			t.Fatalf("burst event %d was rejected", i)
		}
	}
	if lim.Allow(now) {
		t.Fatal("event beyond burst was allowed")
	}
	if !lim.Allow(now.Add(100 * time.Millisecond)) {
		t.Fatal("event after refill was rejected")
	}
}

func FuzzParseCtlEvent(f *testing.F) {
	seeds := []string{
		`{"type":"heartbeat"}`,
		`{"type":"status","session":"abcd2345","state":"working","source":"output","reason":"ok"}`,
		`{"type":"status","session":"abcd2345","state":"needs_input","source":"notify","reason":"hello\u001b[31m\u202eworld"}`,
		`{"type":"status","state":"exited","source":"hook"}`,
		`{"type":"alert","session":"abcd2345","kind":"needs_input","detail":"Proceed?"}`,
		`{"type":"agent_id","session":"abcd2345","id":"3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f","id_source":"clear"}`,
		`{"type":"ended","session":"abcd2345","exit_code":0,"signal":""}`,
		`{"type":"ports","ports":[{"port":8080,"address":"127.0.0.1","pid":12,"process":"srv"}]}`,
		`{"type":"resize","cols":120,"rows":40}`,
		`not json`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		ev, err := ParseEvent(data)
		if err != nil {
			return
		}
		if utf8.RuneCountInString(ev.Reason) > maxReasonRunes {
			t.Fatalf("reason exceeds %d runes: %d", maxReasonRunes, utf8.RuneCountInString(ev.Reason))
		}
		if utf8.RuneCountInString(ev.Detail) > maxDetailRunes {
			t.Fatalf("detail exceeds %d runes: %d", maxDetailRunes, utf8.RuneCountInString(ev.Detail))
		}
		for _, r := range ev.Reason + ev.Detail + ev.Signal + ev.Session {
			if api.Unclean(r) {
				t.Fatalf("unclean rune %U remained after ParseEvent", r)
			}
		}
		if len(ev.Ports) > MaxClaimPorts {
			t.Fatalf("too many ports: %d", len(ev.Ports))
		}
		if ev.Type == "status" {
			if ev.State != api.StateWorking && ev.State != api.StateIdle && ev.State != api.StateNeedsInput {
				t.Fatalf("unexpected status state %q", ev.State)
			}
			if !sourcePattern.MatchString(ev.Source) {
				t.Fatalf("unexpected status source %q", ev.Source)
			}
		}
	})
}
