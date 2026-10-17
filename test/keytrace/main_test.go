package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/diag"
)

const ms1 = 1_000_000 // one millisecond in ns

// One key on each side, with a host clock 50 ms ahead of the client's.
// Client: read at 0, sent at 1 ms, the echo arrives at 31 ms and is written
// at 32 ms. Host (+50 ms): the bridge reads the key at 61, the holder at 62,
// gets the lock at 64, ends the PTY write at 65, and reads the echo at 70.
func sample() (client, host []diag.Record) {
	const off = 50 * ms1
	client = []diag.Record{
		{T: 0, Ev: "start", Side: "client"},
		{T: 0, Ev: "stdin", Read: 1, N: 1},
		{T: 1 * ms1, Ev: "key", Trace: "ab.1", Key: 1, Read: 1, N: 1},
		{T: 32 * ms1, Ev: "echo", Trace: "ab.1", Key: 1, Recv: 31 * ms1},
		{T: 40 * ms1, Ev: "ping", Trace: "ab.1", RTT: 20 * ms1, Host: 1, Offset: off},
		{T: 80 * ms1, Ev: "ping", Trace: "ab.1", RTT: 40 * ms1, Host: 1, Offset: off + 9*ms1},
	}
	host = []diag.Record{
		{T: 60 * ms1, Ev: "start", Side: "host", Trace: "ab.1"},
		{T: 65 * ms1, Ev: "key", Trace: "ab.1", Key: 1, Recv: 62 * ms1, Bridge: 61 * ms1, Wait: 2 * ms1},
		{T: 70 * ms1, Ev: "read", Trace: "ab.1", Key: 1, N: 10, Wait: ms1, Hold: 3 * ms1, Feed: 2 * ms1},
		{T: 71 * ms1, Ev: "tick", Wait: 0, Hold: ms1 / 2},
		{T: 0, Ev: "sample", Mem: &diag.Memory{Current: 100 << 20, MemSome: 0, MajFault: 10}},
		{T: 1000 * ms1, Ev: "sample", Mem: &diag.Memory{Current: 200 << 20, Peak: 300 << 20, MemSome: 250 * ms1, MajFault: 110, OOMKill: 1}},
	}
	return client, host
}

func TestReport(t *testing.T) {
	client, host := sample()
	var out bytes.Buffer
	report(&out, client, host)
	got := out.String()
	for step, want := range map[string]string{
		"client: stdin read to key sent":                        "1.00",
		"client: key sent to its echo arrived":                  "30.00",
		"client: echo arrived to written":                       "1.00",
		"total: stdin read to echo written":                     "32.00",
		"host: bridge read to holder read":                      "1.00",
		"host: wait for the lock in input()":                    "2.00",
		"host: holder read to PTY write end":                    "3.00",
		"host: PTY write end to next PTY read":                  "5.00",
		"network: round trip of a key, without the host":        "21.00",
		"network: key sent to bridge read (clock offset)":       "10.00",
		"network: next PTY read to echo arrived (clock offset)": "11.00",
		"host: output() holds the lock":                         "3.00",
		"host: screen model feed":                               "2.00",
		"host: tick() holds the lock":                           "0.50",
		"net: ping round trip":                                  "40.00",
	} {
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(step) + ` +\d+ +\S+ +\S+ +\S+ +` + regexp.QuoteMeta(want) + `$`)
		if !re.MatchString(got) {
			t.Errorf("no line %q with a max of %s in:\n%s", step, want, got)
		}
	}
	for _, want := range []string{"ab.1: 50.00 ms, from 2 pings", "at most 300.0 MiB", "stalls 25 % (some)", "100 major faults", "stopped 1 times"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
}

// Keys typed before any output share the first echo after them, and an
// older host gives no offset.
func TestReportWithoutTheHost(t *testing.T) {
	client := []diag.Record{
		{Ev: "start"},
		{T: 0, Ev: "stdin", Read: 1},
		{T: 1 * ms1, Ev: "key", Trace: "x.1", Key: 1, Read: 1},
		{T: 2 * ms1, Ev: "stdin", Read: 2},
		{T: 3 * ms1, Ev: "key", Trace: "x.1", Key: 2, Read: 2},
		{T: 12 * ms1, Ev: "echo", Trace: "x.1", Key: 2, Recv: 11 * ms1},
		{T: 20 * ms1, Ev: "ping", RTT: 5 * ms1},
	}
	var out bytes.Buffer
	report(&out, client, nil)
	got := out.String()
	if !regexp.MustCompile(`(?m)^client: key sent to its echo arrived +2 +8\.00 +10\.00 +10\.00 +10\.00$`).MatchString(got) {
		t.Errorf("the keys do not share the echo:\n%s", got)
	}
	for _, want := range []string{"no pong carried the host's time", "no memory samples"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
}

// Each attach has its own clock offset, as two hosts, or one host before and
// after a restart, do. Within an attach, a key uses the pings nearest it:
// the clocks drift apart over a long attach.
func TestOffsetsPerAttach(t *testing.T) {
	const s = 1000 * ms1
	client := []diag.Record{{Ev: "start"}}
	host := []diag.Record{}
	// Attach ab.1: the host is 50 ms ahead for the first 10 s, with short
	// round trips, then 60 ms ahead from 100 s on, with longer ones.
	for i := range int64(10) {
		client = append(client,
			diag.Record{T: i * s, Ev: "ping", Trace: "ab.1", RTT: 5 * ms1, Host: 1, Offset: 50 * ms1},
			diag.Record{T: 100*s + i*s, Ev: "ping", Trace: "ab.1", RTT: 10 * ms1, Host: 1, Offset: 60 * ms1})
	}
	// Attach cd.1, to another host: 20 ms behind.
	client = append(client, diag.Record{T: 0, Ev: "ping", Trace: "cd.1", RTT: 5 * ms1, Host: 1, Offset: -20 * ms1})
	// One key in each, 10 ms on the way up: at 105 s in ab.1, and at 1 s in cd.1.
	for _, k := range []struct {
		trace string
		at    int64
		off   int64
	}{{"ab.1", 105 * s, 60 * ms1}, {"cd.1", s, -20 * ms1}} {
		client = append(client, diag.Record{T: k.at, Ev: "key", Trace: k.trace, Key: 1})
		host = append(host, diag.Record{T: k.at + k.off + 12*ms1, Ev: "key", Trace: k.trace, Key: 1,
			Bridge: k.at + k.off + 10*ms1, Recv: k.at + k.off + 11*ms1})
	}
	var out bytes.Buffer
	report(&out, client, host)
	got := out.String()
	if !regexp.MustCompile(`(?m)^network: key sent to bridge read \(clock offset\) +2 +10\.00 +10\.00 +10\.00 +10\.00$`).MatchString(got) {
		t.Errorf("the keys do not use their own attach's nearest offset:\n%s", got)
	}
	for _, want := range []string{"ab.1: 50.00 ms, from 20 pings", "cd.1: -20.00 ms, from 1 ping\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.jsonl")
	if err := os.WriteFile(good, []byte(`{"t":1,"cat":"attach","ev":"key","key":3}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := readFile(good)
	if err != nil || len(recs) != 1 || recs[0].Key != 3 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{}\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFile(bad); err == nil || !strings.Contains(err.Error(), "bad.jsonl:2") {
		t.Fatalf("err = %v, want the bad line", err)
	}
}
