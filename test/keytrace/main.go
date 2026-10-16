// Command keytrace reads the debug traces of an attach, the client's file and
// the host's, joins their records by trace and key, and prints percentiles
// for each step a key takes from the keyboard to its echo on the screen.
// docs/contributing/debug-trace.md says how to record the files and how to
// read the result.
//
//	go run ./test/keytrace <client file> [<host file>...]
//
// Give the host's old file (debug.jsonl.1) too, before the current one, to
// cover a longer time.
package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: keytrace <client file> [<host file>...]")
		os.Exit(2)
	}
	files := make([][]diag.Record, 0, len(os.Args)-1)
	for _, p := range os.Args[1:] {
		recs, err := readFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "keytrace:", err)
			os.Exit(1)
		}
		files = append(files, recs)
	}
	report(os.Stdout, files[0], slices.Concat(files[1:]...))
}

func readFile(path string) ([]diag.Record, error) {
	f, err := os.Open(path) //nolint:gosec // G703: the user names the file to read
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var out []diag.Record
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		var r diag.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// ref names a key: the attach's trace ID and the key's number in it.
type ref struct {
	trace string
	key   int64
}

// readRef names a stdin read: the client's start record, counted from 1,
// and the read's number after it.
type readRef struct {
	start int
	read  int64
}

// trace holds the records of both sides, indexed for the join.
type trace struct {
	keys     []diag.Record            // the client's keys, in order
	reads    map[readRef]diag.Record  // the client's stdin reads
	readOf   map[ref]readRef          // a key's stdin read
	echoes   map[string][]diag.Record // the client's echoes, by trace, in key order
	hostKeys map[ref]diag.Record
	ptyReads map[ref]diag.Record // the host's first PTY read after each key
	host     []diag.Record       // all the host's records
	pings    []diag.Record
	// timed holds the pings that carry the host's time, by trace, in time
	// order. Each attach may reach another host, or a host that restarted.
	timed map[string][]diag.Record
}

func index(client, host []diag.Record) *trace {
	t := &trace{reads: map[readRef]diag.Record{}, readOf: map[ref]readRef{}, echoes: map[string][]diag.Record{},
		hostKeys: map[ref]diag.Record{}, ptyReads: map[ref]diag.Record{}, host: host, timed: map[string][]diag.Record{}}
	// Read numbers start again at each start record.
	starts := 0
	for _, r := range client {
		switch r.Ev {
		case "start":
			starts++
		case "stdin":
			t.reads[readRef{starts, r.Read}] = r
		case "key":
			t.keys = append(t.keys, r)
			t.readOf[ref{r.Trace, r.Key}] = readRef{starts, r.Read}
		case "echo":
			t.echoes[r.Trace] = append(t.echoes[r.Trace], r)
		case "ping":
			t.pings = append(t.pings, r)
			if r.Host != 0 {
				t.timed[r.Trace] = append(t.timed[r.Trace], r)
			}
		}
	}
	for _, r := range host {
		switch {
		case r.Ev == "key":
			t.hostKeys[ref{r.Trace, r.Key}] = r
		case r.Ev == "read" && r.Key != 0:
			t.ptyReads[ref{r.Trace, r.Key}] = r
		}
	}
	for _, ps := range t.timed {
		slices.SortFunc(ps, func(a, b diag.Record) int { return cmp.Compare(a.T, b.T) })
	}
	return t
}

// nearPings is how many pings around a key offsetAt looks at, as many as
// NTP's clock filter keeps.
const nearPings = 8

// offsetAt is the host's clock minus the client's, for the attach with
// the trace ID trace, at the client's time at. The clocks drift apart, so
// it looks at the pings nearest that time, and, as NTP does, it trusts the
// one with the shortest round trip.
func (t *trace) offsetAt(trace string, at int64) (int64, bool) {
	ps := t.timed[trace]
	if len(ps) == 0 {
		return 0, false
	}
	i, _ := slices.BinarySearchFunc(ps, at, func(p diag.Record, at int64) int { return cmp.Compare(p.T, at) })
	hi := min(len(ps), max(0, i-nearPings/2)+nearPings)
	best := ps[max(0, hi-nearPings)]
	for _, p := range ps[max(0, hi-nearPings):hi] {
		if p.RTT < best.RTT {
			best = p
		}
	}
	return best.Offset, true
}

// echoOf is the first output after a key: the first echo of its own key or
// of a later one in the same attach.
func (t *trace) echoOf(k diag.Record) (diag.Record, bool) {
	for _, e := range t.echoes[k.Trace] {
		if e.Key >= k.Key {
			return e, true
		}
	}
	return diag.Record{}, false
}

// steps works out each step's samples, in ns, in the order they print.
func (t *trace) steps() []step {
	s := newSteps("client: stdin read to key sent", "client: key sent to its echo arrived", "client: echo arrived to written",
		"total: stdin read to echo written", "host: bridge read to holder read", "host: wait for the lock in input()",
		"host: holder read to PTY write end", "host: PTY write end to next PTY read", "network: round trip of a key, without the host",
		"network: key sent to bridge read (clock offset)", "network: next PTY read to echo arrived (clock offset)")
	for _, k := range t.keys {
		read, hasRead := t.reads[t.readOf[ref{k.Trace, k.Key}]]
		echo, hasEcho := t.echoOf(k)
		if hasRead {
			s.add(0, k.T-read.T)
		}
		if hasEcho {
			s.add(1, echo.Recv-k.T)
			s.add(2, echo.T-echo.Recv)
			if hasRead {
				s.add(3, echo.T-read.T)
			}
		}
		hk, ok := t.hostKeys[ref{k.Trace, k.Key}]
		if !ok {
			continue
		}
		s.add(4, hk.Recv-hk.Bridge)
		s.add(5, hk.Wait)
		s.add(6, hk.T-hk.Recv)
		off, hasOffset := t.offsetAt(k.Trace, k.T)
		if hasOffset {
			s.add(9, hk.Bridge-off-k.T)
		}
		pr, ok := t.ptyReads[ref{k.Trace, k.Key}]
		if !ok {
			continue
		}
		s.add(7, pr.T-hk.T)
		if hasEcho && echo.Key == k.Key {
			s.add(8, (echo.Recv-k.T)-(pr.T-hk.Bridge))
			if hasOffset {
				s.add(10, echo.Recv-(pr.T-off))
			}
		}
	}
	return s.list
}

// hostSteps are the holder's lock and screen model for all its output.
func (t *trace) hostSteps() []step {
	s := newSteps("host: output() wait for the lock", "host: output() holds the lock", "host: screen model feed",
		"host: tick() wait for the lock", "host: tick() holds the lock", "net: ping round trip")
	for _, r := range t.host {
		switch r.Ev {
		case "read":
			s.add(0, r.Wait)
			s.add(1, r.Hold)
			s.add(2, r.Feed)
		case "tick":
			s.add(3, r.Wait)
			s.add(4, r.Hold)
		}
	}
	for _, p := range t.pings {
		s.add(5, p.RTT)
	}
	return s.list
}

type step struct {
	name string
	ns   []int64
}

type steps struct{ list []step }

func newSteps(names ...string) *steps {
	s := &steps{}
	for _, n := range names {
		s.list = append(s.list, step{name: n})
	}
	return s
}

func (s *steps) add(i int, ns int64) { s.list[i].ns = append(s.list[i].ns, ns) }

// percentile is the nearest-rank percentile p of sorted samples.
func percentile(sorted []int64, p float64) int64 {
	i := int(p*float64(len(sorted))+0.999999) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

func ms(ns int64) string { return fmt.Sprintf("%.2f", float64(ns)/float64(time.Millisecond)) }

func printSteps(w io.Writer, list []step) {
	for _, s := range list {
		if len(s.ns) == 0 {
			fmt.Fprintf(w, "%-56s %6d\n", s.name, 0)
			continue
		}
		slices.Sort(s.ns)
		fmt.Fprintf(w, "%-56s %6d %9s %9s %9s %9s\n", s.name, len(s.ns),
			ms(percentile(s.ns, 0.5)), ms(percentile(s.ns, 0.9)), ms(percentile(s.ns, 0.99)), ms(s.ns[len(s.ns)-1]))
	}
}

func report(w io.Writer, client, host []diag.Record) {
	t := index(client, host)
	fmt.Fprintf(w, "%-56s %6s %9s %9s %9s %9s\n", "step (ms)", "count", "p50", "p90", "p99", "max")
	printSteps(w, t.steps())
	printSteps(w, t.hostSteps())
	t.offsets(w)
	memory(w, host)
}

// offsets prints each attach's clock offset at its ping with the shortest
// round trip. The steps use the offset from the pings nearest each key.
func (t *trace) offsets(w io.Writer) {
	if len(t.timed) == 0 {
		fmt.Fprintln(w, "\nno pong carried the host's time: the steps marked (clock offset) are empty")
		return
	}
	fmt.Fprintln(w, "\nclock offset (host minus client) of each attach, at its shortest round trip:")
	for _, id := range slices.Sorted(maps.Keys(t.timed)) {
		ps := t.timed[id]
		best := slices.MinFunc(ps, func(a, b diag.Record) int { return cmp.Compare(a.RTT, b.RTT) })
		pings := "pings"
		if len(ps) == 1 {
			pings = "ping"
		}
		fmt.Fprintf(w, "  %s: %s ms, from %d %s\n", id, ms(best.Offset), len(ps), pings)
	}
}

// memory prints the session's memory: the largest use, and the worst second
// of pressure and of major faults.
func memory(w io.Writer, host []diag.Record) {
	var prev *diag.Record
	var peak, some, full, faults, refaults int64
	var n int
	for i := range host {
		r := &host[i]
		if r.Ev != "sample" || r.Mem == nil {
			continue
		}
		n++
		peak = max(peak, r.Mem.Current, r.Mem.Peak)
		if prev != nil && r.T > prev.T {
			// The share of the second that some or all tasks stalled on memory.
			dt := r.T - prev.T
			some = max(some, 1000*(r.Mem.MemSome-prev.Mem.MemSome)/dt)
			full = max(full, 1000*(r.Mem.MemFull-prev.Mem.MemFull)/dt)
			faults = max(faults, (r.Mem.MajFault-prev.Mem.MajFault)*int64(time.Second)/dt)
			refaults = max(refaults, (r.Mem.Refault-prev.Mem.Refault)*int64(time.Second)/dt)
		}
		prev = r
	}
	if n == 0 {
		fmt.Fprintln(w, "no memory samples: the holder runs outside a unit of its own")
		return
	}
	fmt.Fprintf(w, "memory: %d samples, at most %.1f MiB; in the worst second, memory stalls %s %% (some) and %s %% (full), "+
		"%d major faults, %d file refaults\n", n, float64(peak)/(1<<20), permille(some), permille(full), faults, refaults)
	if last := prev.Mem; last.OOMKill > 0 {
		fmt.Fprintf(w, "memory: the unit's processes were stopped %d times at the cap (oom_kill)\n", last.OOMKill)
	}
}

func permille(n int64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/10), ".0") }
