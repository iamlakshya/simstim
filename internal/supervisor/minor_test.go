package supervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Events over the rate leave a trace: an audit line with their count.
func TestDroppedEventsReachTheAuditLog(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	for range 3 * eventRate {
		h.send(t, proto.CtlEvent{Event: proto.EventHeartbeat})
	}
	waitFor(t, "the audit line", func() bool {
		b, _ := os.ReadFile(paths.AuditLog(h.id))
		return strings.Contains(string(b), `"event":"events_dropped"`)
	})
}

// The guest's words in a failed start reach the audit log cleaned, cut
// short and marked as the guest's.
func TestAFailedStartMarksTheGuestsWords(t *testing.T) {
	h := hostileStart(t)
	req := h.readReq(t)
	msg := "no\x1b]52;c;aGk=\x07 such " + strings.Repeat("x", 500)
	_ = h.w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: &proto.Error{Code: "start_failed", Message: msg, Hint: "run this: rm -rf ~"}})
	err := h.waitEnd(t, 10*time.Second)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Hint == "" || strings.Contains(pe.Hint, "rm -rf") {
		t.Fatalf("a failed start = %v, want the trusted side's hint", err)
	}
	b, _ := os.ReadFile(paths.AuditLog(h.id))
	var line struct {
		Event  string         `json:"event"`
		Fields map[string]any `json:"fields"`
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if json.Unmarshal([]byte(l), &line) == nil && line.Event == "start_failed" {
			break
		}
	}
	says, _ := line.Fields["guest_says"].(string)
	if line.Event != "start_failed" || says == "" || len([]rune(says)) > maxReason || strings.ContainsAny(says, "\x1b\x07") {
		t.Errorf("the audit line = %+v, want the guest's words cleaned, cut and marked", line)
	}
}

// A bare Enter that send types counts as a person who typed, as in the
// holder.
func TestSendCountsTheEnter(t *testing.T) {
	sup := make(chan *Supervisor, 1)
	testStarted = func(s *Supervisor) { sup <- s }
	t.Cleanup(func() { testStarted = nil })
	id, g, _ := supervised(t)
	s := <-sup
	go func() {
		st := <-g.holders
		gr, gw := bufio.NewReader(st), proto.NewWriter(st)
		_, _ = proto.Read(gr)
		_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
		f, _ := proto.Read(gr)
		var req proto.Request
		_ = json.Unmarshal(f.Payload, &req)
		_ = gw.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{"bytes":1}`)})
	}()
	if _, err := host.HolderRequest(id, "send", map[string]any{"text": "", "enter": true}, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	typed := s.mark.typed
	s.mu.Unlock()
	if typed.IsZero() {
		t.Error("a bare Enter did not count as typing")
	}
}

// A supervisor that cannot start says what to try.
func TestRunSaysWhatToTry(t *testing.T) {
	testFolders(t)
	err := Run("nosuchid")
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Hint == "" {
		t.Errorf("Run of no session = %v, want an error with a hint", err)
	}
}
