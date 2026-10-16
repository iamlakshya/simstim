//go:build !windows

package client_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

// With the debug trace on, the attach stays byte-exact both ways, and the
// client and the holder each write their records, which join by trace and
// key and hold no key or output bytes.
func TestAttachIsByteExactWithTheTraceOn(t *testing.T) {
	r := newAttachRun(t)
	file := filepath.Join(t.TempDir(), "client.jsonl")
	t.Setenv(diag.EnvFile, file)
	var id string
	checkByteExact(t, r, func(session string) {
		id = session
		// Stay attached until a timed ping has come back.
		waitFile(t, file, `"ev":"ping"`)
	})
	hosttest.WaitFor(t, 10*time.Second, "the host's trace to stop", func() bool {
		return strings.Contains(r.h.HolderLog(id), "debug trace off")
	})
	client, host := traceFile(t, file), traceFile(t, paths.DebugTrace(id))

	start, conn := client.one(t, "start"), client.one(t, "conn")
	if start.Side != "client" || start.Session != id || conn.Trace == "" || !conn.HostTrace {
		t.Fatalf("start = %+v, conn = %+v", start, conn)
	}
	if hs := host.one(t, "start"); hs.Side != "host" || hs.Trace != conn.Trace {
		t.Fatalf("the host's start = %+v, want trace %s", hs, conn.Trace)
	}
	keys, hostKeys := client.all("key"), host.all("key")
	if len(client.all("stdin")) < 2 || len(keys) < 2 || len(hostKeys) != len(keys) {
		t.Fatalf("%d reads and %d keys on the client, %d keys on the host", len(client.all("stdin")), len(keys), len(hostKeys))
	}
	for i, k := range keys {
		hk := hostKeys[i]
		if k.Trace != conn.Trace || k.Key != int64(i+1) || hk.Trace != k.Trace || hk.Key != k.Key || hk.N != k.N || k.Read < 1 {
			t.Errorf("key %d: client %+v, host %+v", i+1, k, hk)
		}
	}
	// The first key, x, makes the agent print: its echo is that output.
	echo := client.one(t, "echo")
	if echo.Key != 1 || echo.Trace != conn.Trace || echo.Recv < keys[0].T || echo.T < echo.Recv || echo.N == 0 {
		t.Errorf("echo = %+v after key %+v", echo, keys[0])
	}
	if read := host.find(func(r diag.Record) bool { return r.Ev == "read" && r.Key == 1 }); read == nil || read.T < hostKeys[0].T {
		t.Errorf("the host's read after key 1 = %+v", read)
	}
	// On one machine, the two clocks agree within half the round trip.
	ping := client.one(t, "ping")
	if ping.Cat != diag.CatNet || ping.Trace != conn.Trace || ping.Host == 0 || ping.RTT <= 0 || 2*abs(ping.Offset) > ping.RTT {
		t.Errorf("ping = %+v", ping)
	}
}

type traceRecords struct {
	raw  []byte
	recs []diag.Record
}

// traceFile reads a trace file, and checks that it is private and holds no
// bytes of the session: the test's keys and output.
func traceFile(t *testing.T, path string) traceRecords {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("%s: %v, %v; want mode 600", path, st, err)
	}
	for _, b := range []string{"hello", "world", "truecolor", "END"} {
		if bytes.Contains(raw, []byte(b)) {
			t.Errorf("%s holds %q", path, b)
		}
	}
	var out []diag.Record
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var r diag.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("%s: line %q: %v", path, sc.Text(), err)
		}
		out = append(out, r)
	}
	return traceRecords{raw: raw, recs: out}
}

func (tr traceRecords) all(ev string) []diag.Record {
	var out []diag.Record
	for _, r := range tr.recs {
		if r.Ev == ev {
			out = append(out, r)
		}
	}
	return out
}

func (tr traceRecords) one(t *testing.T, ev string) diag.Record {
	t.Helper()
	all := tr.all(ev)
	if len(all) == 0 {
		t.Fatalf("no %s record in %s", ev, tr.raw)
	}
	return all[0]
}

func (tr traceRecords) find(match func(diag.Record) bool) *diag.Record {
	for i := range tr.recs {
		if match(tr.recs[i]) {
			return &tr.recs[i]
		}
	}
	return nil
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
