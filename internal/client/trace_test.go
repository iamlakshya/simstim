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

// startTestDebug turns on the client's debug with cats, into a file in a
// new folder, until the end of the test.
func startTestDebug(t *testing.T, cats string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, err := diag.ParseCats(cats)
	if err != nil {
		t.Fatal(err)
	}
	path, err := StartDebug(DebugOptions{Cats: c, File: filepath.Join(t.TempDir(), "client.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { StopDebug(os.Stderr) })
	return path
}

func newTestTrace(t *testing.T) *attachTrace {
	t.Helper()
	startTestDebug(t, "attach,net")
	tr := openAttachTrace("s1")
	if tr == nil {
		t.Fatal("no trace with debug on")
	}
	return tr
}

// closeTrace ends the client's debug and returns the trace's records after
// the attach's start record, without the stop record.
func closeTrace(t *testing.T, tr *attachTrace) []diag.Record {
	t.Helper()
	var warn bytes.Buffer
	StopDebug(&warn)
	if warn.Len() > 0 {
		t.Errorf("a complete trace warns: %s", warn.Bytes())
	}
	f, err := os.Open(tr.f.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []diag.Record
	started := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r diag.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		switch {
		case r.Ev == diag.EvStart && r.Session == "s1":
			started = true
		case started && r.Ev != diag.EvStop && r.Ev != diag.EvLog:
			out = append(out, r)
		}
	}
	if !started {
		t.Fatalf("records = %+v, want the attach's start record", out)
	}
	return out
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
	if len(tr.rtts) != 2 {
		t.Errorf("the trace keeps %d round trips, want 2", len(tr.rtts))
	}
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

// The client's file keeps the records of its categories only: with net
// alone, the keys are left out and the host is asked for nothing.
func TestTheTraceKeepsItsCategories(t *testing.T) {
	startTestDebug(t, "net")
	tr := openAttachTrace("s1")
	if tr == nil || tr.host != 0 {
		t.Fatalf("trace = %+v, want one with no host categories", tr)
	}
	c := &Conn{Features: []string{"trace"}, tr: &connTrace{t: tr, id: "ab.1"}}
	if id, cats := c.startTrace(); id != "" || cats != nil {
		t.Errorf("an attach asked the host for %q %v with net alone", id, cats)
	}
	tr.stdin(3)
	tr.pong("ab.1", proto.Pong(tr.ping(), diag.Now()))
	recs := closeTrace(t, tr)
	if len(recs) != 1 || recs[0].Ev != "ping" {
		t.Fatalf("records = %+v, want a ping alone", recs)
	}
}

// With the attach category, an attach asks the host for its categories
// among those on.
func TestAnAttachAsksTheHostForItsCategories(t *testing.T) {
	startTestDebug(t, "attach,launcher,runtime")
	tr := openAttachTrace("s1")
	c := &Conn{Features: []string{"trace"}, tr: &connTrace{t: tr, id: "ab.1"}}
	id, cats := c.startTrace()
	if id != "ab.1" || strings.Join(cats, ",") != "attach,runtime" {
		t.Fatalf("the attach asks for %q %v", id, cats)
	}
}

func TestTraceIsOffWithoutDebug(t *testing.T) {
	tr := openAttachTrace("s1")
	if tr != nil {
		t.Fatalf("trace = %v with debug off", tr)
	}
	// Every hook takes a nil trace.
	tr.stdin(1)
	tr.took()
	tr.drawn(0, 1)
	if tr.pingEvery() != pingEvery {
		t.Error("the heartbeat changed with the trace off")
	}
	if id, cats := (&Conn{}).startTrace(); id != "" || cats != nil {
		t.Error("an attach without a trace sent a trace ID")
	}
	// The launcher's category alone traces no attach.
	startTestDebug(t, "launcher")
	if openAttachTrace("s1") != nil {
		t.Error("an attach is traced with the launcher's category alone")
	}
}

// A debug file that could not take all its records says so at the end,
// here because a folder is in the way of its turnover.
func TestADebugFileCutShortWarns(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "client.jsonl")
	if err := os.MkdirAll(filepath.Join(path+".1", "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := debugFileMax
	debugFileMax = 100
	t.Cleanup(func() { debugFileMax = old })
	if _, err := StartDebug(DebugOptions{Cats: diag.AllCats &^ diag.CatsOf([]string{diag.CatRuntime}), File: path}); err != nil {
		t.Fatal(err)
	}
	tr := openAttachTrace("s1")
	for range 5 {
		tr.stdin(1)
	}
	var warn bytes.Buffer
	StopDebug(&warn)
	for _, want := range []string{"Warning: the debug file " + path + " is not complete", "turn over the trace file", "\nTry: ", "--debug-file"} {
		if !strings.Contains(warn.String(), want) {
			t.Errorf("no %q in the warning %q", want, warn.String())
		}
	}
}

func TestDebugToAFileItCannotWrite(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, err := StartDebug(DebugOptions{Cats: diag.AllCats, File: filepath.Join(t.TempDir(), "no", "such", "trace.jsonl")})
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Hint, diag.EnvFile) || !strings.Contains(ue.Hint, "--debug-file") {
		t.Fatalf("err = %v, want a hint that names --debug-file and %s", err, diag.EnvFile)
	}
	if DebugFile() != nil {
		t.Fatal("debug is on after a failed start")
	}
}

// A debug file outside the debug folder is listed in it, once, for the
// bundle.
func TestAFileOutsideTheFolderIsListed(t *testing.T) {
	path := startTestDebug(t, "net")
	StopDebug(os.Stderr)
	if _, err := StartDebug(DebugOptions{Cats: diag.AllCats &^ diag.CatsOf([]string{diag.CatRuntime}), File: path}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(DebugDir(), diag.OutsideList))
	if err != nil || string(b) != path+"\n" {
		t.Fatalf("%s = %q, %v; want the path once", diag.OutsideList, b, err)
	}
}

func TestParseMaster(t *testing.T) {
	if ok, pid := parseMaster("Master running (pid=4242)\r\n"); !ok || pid != 4242 {
		t.Errorf("running = %v, pid = %d", ok, pid)
	}
	if ok, pid := parseMaster("Control socket connect(/x): No such file or directory\n"); ok || pid != 0 {
		t.Errorf("with no master: running = %v, pid = %d", ok, pid)
	}
}
