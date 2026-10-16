package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/proto"
)

func newTestTrace(t *testing.T) *attachTrace {
	t.Helper()
	t.Setenv(diag.EnvFile, filepath.Join(t.TempDir(), "client.jsonl"))
	tr, err := openAttachTrace("s1")
	if err != nil || tr == nil {
		t.Fatalf("trace = %v, %v", tr, err)
	}
	return tr
}

// closeTrace closes the trace and returns its records after the start record.
func closeTrace(t *testing.T, tr *attachTrace) []diag.Record {
	t.Helper()
	var warn bytes.Buffer
	tr.close(&warn)
	if warn.Len() > 0 {
		t.Errorf("a complete trace warns: %s", warn.Bytes())
	}
	f, err := os.Open(tr.f.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []diag.Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r diag.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if len(out) == 0 || out[0].Ev != "start" || out[0].Session != "s1" {
		t.Fatalf("records = %+v, want a start record first", out)
	}
	return out[1:]
}

func TestClockOffset(t *testing.T) {
	// Sent at 100 and answered at 300 on the client's clock. The host read
	// 1200 on its own. The midpoint, 200, is 1200 on the host: 1000 ahead.
	if got := clockOffset(100, 300, 1200); got != 1000 {
		t.Fatalf("offset = %d, want 1000", got)
	}
	if got := clockOffset(1000, 1400, 200); got != -1000 {
		t.Fatalf("offset = %d, want -1000", got)
	}
}

// A pong from an older bridge has no host time: the round trip is still
// recorded, without an offset. A pong for an unknown ping is ignored.
func TestPongs(t *testing.T) {
	tr := newTestTrace(t)
	tr.pong("ab.1", tr.ping())                              // an old bridge's echo
	tr.pong("ab.2", proto.Pong(tr.ping(), diag.Now()+5000)) // a new bridge's answer
	tr.pong("ab.2", proto.Pong(proto.EncodePing(99), 1))    // never sent
	tr.pong("ab.2", []byte("p"))                            // not a timed ping
	recs := closeTrace(t, tr)
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want 2 pings", recs)
	}
	old, cur := recs[0], recs[1]
	if old.Ev != "ping" || old.Cat != diag.CatNet || old.Trace != "ab.1" || old.Ping != 1 || old.Host != 0 || old.Offset != 0 || old.RTT < 0 {
		t.Errorf("the old bridge's ping = %+v", old)
	}
	if cur.Trace != "ab.2" || cur.Ping != 2 || cur.Host == 0 || cur.Offset < 5000-cur.RTT || cur.Offset > 5000+cur.RTT {
		t.Errorf("the new bridge's ping = %+v, want an offset of about 5000", cur)
	}
}

// The first output frame after a key is its echo, recorded once the frame
// is written to the terminal.
func TestEchoes(t *testing.T) {
	tr := newTestTrace(t)
	ct := &connTrace{t: tr, id: "ab.1"}
	ct.arrived(proto.EncodeOutput(10, []byte("before any key")))
	ct.waiting.Store(2)
	ct.arrived(proto.EncodeOutput(100, []byte("echo")))
	ct.arrived(proto.EncodeOutput(104, []byte("more")))
	tr.drawn(10, 14)
	tr.drawn(100, 4)
	tr.drawn(104, 4)
	recs := closeTrace(t, tr)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want one echo", recs)
	}
	if e := recs[0]; e.Ev != "echo" || e.Trace != "ab.1" || e.Key != 2 || e.N != 4 || e.Recv == 0 || e.T < e.Recv {
		t.Fatalf("echo = %+v", e)
	}
}

func TestTraceIsOffWithoutTheVariable(t *testing.T) {
	t.Setenv(diag.EnvFile, "")
	tr, err := openAttachTrace("s1")
	if err != nil || tr != nil {
		t.Fatalf("trace = %v, %v", tr, err)
	}
	// Every hook takes a nil trace.
	tr.stdin(1)
	tr.took()
	tr.drawn(0, 1)
	tr.close(nil) // a nil trace writes no warning
	if tr.pingEvery() != pingEvery {
		t.Error("the heartbeat changed with the trace off")
	}
	if c := (&Conn{}); c.startTrace() != "" {
		t.Error("an attach without a trace sent a trace ID")
	}
}

// A trace that could not write all its records says so after the attach,
// here because a folder is in the way of its turnover.
func TestATraceCutShortWarns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.jsonl")
	if err := os.MkdirAll(filepath.Join(path+".1", "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := diag.Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	tr := &attachTrace{f: f, sent: map[uint64]int64{}}
	for range 5 {
		tr.stdin(1)
	}
	var warn bytes.Buffer
	tr.close(&warn)
	for _, want := range []string{"Warning: the debug trace in " + path + " is not complete", "turn over the trace file", "\nTry: ", diag.EnvFile} {
		if !strings.Contains(warn.String(), want) {
			t.Errorf("no %q in the warning %q", want, warn.String())
		}
	}
}

func TestTraceToAFileItCannotWrite(t *testing.T) {
	t.Setenv(diag.EnvFile, filepath.Join(t.TempDir(), "no", "such", "trace.jsonl"))
	_, err := openAttachTrace("s1")
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Hint, diag.EnvFile) {
		t.Fatalf("err = %v, want a hint that names %s", err, diag.EnvFile)
	}
}
