package host_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A traced attach makes the holder write the session's debug trace: when the
// bridge and the holder read each key, the wait for the lock, the end of
// the PTY write and the next PTY read. The trace stops at the detach.
func TestTracedAttachWritesTheHostTrace(t *testing.T) {
	h := hosttest.New(t)
	s := start(t, "traced", "/bin/sh", "-c", "stty raw -echo; printf 'READY'; head -c 6 >/dev/null; printf 'ECHOED'; exec sleep 60")
	waitOutput(t, s.ID, "READY")
	b := newRawBridge(t)
	b.sendJSON(proto.TAttach, proto.Attach{Session: s.ID, Cols: 80, Rows: 24, LastOffset: -1, Trace: "ab12.1"})
	b.next(proto.TAttached)
	b.output(0, "READY")
	before := diag.Now()
	b.send(proto.TInput, []byte("SECR"))
	b.send(proto.TInput, []byte("ET"))
	b.output(5, "ECHOED")
	b.send(proto.TDetach, nil)
	hosttest.WaitFor(t, 10*time.Second, "the trace to stop", func() bool {
		return strings.Contains(h.HolderLog(s.ID), "debug trace off")
	})

	path := paths.DebugTrace(s.ID)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECR", "READY", "ECHOED"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("the trace holds the session's bytes %q", secret)
		}
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the trace's mode: %v, %v", st, err)
	}
	var start diag.Record
	var keys []diag.Record
	var echo *diag.Record
	for _, r := range records(t, raw) {
		switch {
		case r.Ev == "start":
			start = r
		case r.Ev == "key":
			keys = append(keys, r)
		case r.Ev == "read" && r.Key != 0:
			echo = &r
		}
	}
	if start.Side != "host" || start.Trace != "ab12.1" || start.Session != s.ID || start.Wall == 0 {
		t.Errorf("start = %+v", start)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %+v, want 2", keys)
	}
	for i, k := range keys {
		if k.Trace != "ab12.1" || k.Key != int64(i+1) || k.Bridge < before || k.Recv < k.Bridge || k.T < k.Recv || k.Wait < 0 {
			t.Errorf("key %d = %+v", i+1, k)
		}
	}
	if keys[0].N != 4 || keys[1].N != 2 {
		t.Errorf("the keys' sizes are %d and %d, want 4 and 2", keys[0].N, keys[1].N)
	}
	// The agent writes only after the second key, so the next read is its.
	if echo == nil || echo.Trace != "ab12.1" || echo.Key != 2 || echo.N == 0 || echo.T < keys[1].T {
		t.Errorf("the read after the keys = %+v", echo)
	}
}

// A timed ping's pong carries the bridge's clock. On one machine, it lies
// between the send and the answer.
func TestBridgeAnswersATimedPingWithItsClock(t *testing.T) {
	hosttest.New(t)
	b := newRawBridge(t)
	before := diag.Now()
	b.send(proto.TPing, proto.EncodePing(42))
	id, at, ok := proto.DecodePong(b.next(proto.TPong).Payload)
	after := diag.Now()
	if !ok || id != 42 || at < before || at > after {
		t.Fatalf("pong = %d at %d, %v; want 42 between %d and %d", id, at, ok, before, after)
	}
}

func records(t *testing.T, raw []byte) []diag.Record {
	t.Helper()
	var out []diag.Record
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var r diag.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}
