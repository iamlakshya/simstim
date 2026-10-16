package host

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// readTrace reads the records of a trace file.
func readTrace(t *testing.T, path string) []diag.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []diag.Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r diag.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

// fakeUnitCgroup makes a cgroup folder for session id's unit, with files, and
// puts the holder in it, for the rest of the test.
func fakeUnitCgroup(t *testing.T, id string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	old := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = old })
	unit := "/user.slice/user-UID.slice/user@UID.service/" + brand.Slice + "/" + UnitName(id)
	dir := filepath.Join(root, unit)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fakeProc(t, map[string]string{"self/cgroup": "0::" + unit + "\n"})
	return dir
}

var memFiles = map[string]string{
	"memory.current":  "3221225472\n",
	"memory.peak":     "4026531840\n",
	"memory.events":   "low 0\nhigh 12\nmax 3\noom 1\noom_kill 1\noom_group_kill 0\n",
	"memory.stat":     "anon 2147483648\nfile 1073741824\nkernel 1000\nworkingset_refault_anon 5\nworkingset_refault_file 4321\npgfault 99999\npgmajfault 765\n",
	"memory.pressure": "some avg10=12.50 avg60=3.00 avg300=1.00 total=2500000\nfull avg10=10.00 avg60=2.00 avg300=0.50 total=2000000\n",
	"cpu.pressure":    "some avg10=1.00 avg60=0.00 avg300=0.00 total=150\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
}

var wantMem = diag.Memory{
	Current: 3221225472, Peak: 4026531840,
	Anon: 2147483648, File: 1073741824, Refault: 4321, MajFault: 765,
	High: 12, Max: 3, OOM: 1, OOMKill: 1,
	MemSome: 2500000000, MemFull: 2000000000, CPUSome: 150000,
}

func TestReadMemoryFromTheSessionsUnit(t *testing.T) {
	const id = "memtest1"
	dir := fakeUnitCgroup(t, id, memFiles)
	if got := unitCgroupDir(id); got != dir {
		t.Fatalf("the unit's folder is %q, want %q", got, dir)
	}
	if got := readMemory(dir); *got != wantMem {
		t.Fatalf("memory = %+v\nwant     %+v", *got, wantMem)
	}
	// An older kernel has no memory.peak, and a host may lack PSI.
	for _, name := range []string{"memory.peak", "memory.pressure", "cpu.pressure"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	got := readMemory(dir)
	if got.Peak != 0 || got.MemSome != 0 || got.CPUSome != 0 || got.Current != wantMem.Current {
		t.Fatalf("without some files, memory = %+v", *got)
	}
	// A holder outside its own unit reads no one's memory.
	fakeProc(t, map[string]string{"self/cgroup": "0::/system.slice/ci-runner.service\n"})
	if got := unitCgroupDir(id); got != "" {
		t.Fatalf("outside its unit, the holder reads %q", got)
	}
}

func TestSampleMemoryWritesASampleEachSecond(t *testing.T) {
	const id = "memtest2"
	fakeUnitCgroup(t, id, memFiles)
	f, err := diag.Open(filepath.Join(t.TempDir(), "debug.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{id: id}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one sample, then stop
	h.sampleMemory(ctx, &holderTrace{f: f})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	recs := readTrace(t, f.Path())
	if len(recs) != 1 || recs[0].Cat != diag.CatMemory || recs[0].Ev != "sample" || recs[0].Mem == nil || *recs[0].Mem != wantMem {
		t.Fatalf("records = %+v", recs)
	}
}

// With the trace on, the holder times its lock from outside it, and the
// first read after a key's PTY write names that key.
func TestLockTimesRecordTheLock(t *testing.T) {
	f, err := diag.Open(filepath.Join(t.TempDir(), "debug.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{}
	h.dbg.tr.Store(&holderTrace{f: f})
	c := &hclient{tr: clientTrace{id: "ab.1"}}

	lt := h.lockTimed()
	time.Sleep(2 * time.Millisecond)
	h.mu.Unlock()
	lt.tick()

	c.bridgeRead(proto.EncodeTime(lt.asked - 100))
	lt = h.lockTimed()
	h.mu.Unlock()
	lt.key(c, 3)

	lt = h.lockTimed()
	h.mu.Unlock()
	lt.read(64, 1234)
	lt = h.lockTimed()
	h.mu.Unlock()
	lt.read(32, 0)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	recs := readTrace(t, f.Path())
	if len(recs) != 4 {
		t.Fatalf("records = %+v", recs)
	}
	tick, key, first, next := recs[0], recs[1], recs[2], recs[3]
	if tick.Ev != "tick" || tick.Hold < int64(2*time.Millisecond) || tick.Wait < 0 {
		t.Errorf("tick = %+v, want a hold of 2 ms or more", tick)
	}
	if key.Ev != "key" || key.Trace != "ab.1" || key.Key != 1 || key.N != 3 || key.Bridge == 0 || key.Recv < key.Bridge || key.T < key.Recv {
		t.Errorf("key = %+v", key)
	}
	if first.Ev != "read" || first.Trace != "ab.1" || first.Key != 1 || first.N != 64 || first.Feed != 1234 || first.T < key.T {
		t.Errorf("the first read after the key = %+v", first)
	}
	if next.Key != 0 || next.Trace != "" {
		t.Errorf("a later read names a key: %+v", next)
	}
}

// A read that returned before a key's PTY write ended is not that key's
// next read, even when it gets the lock after input() lets it go. The key
// stays for the first read after its write.
func TestAKeyTakesOnlyALaterRead(t *testing.T) {
	f, err := diag.Open(filepath.Join(t.TempDir(), "debug.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{}
	h.dbg.tr.Store(&holderTrace{f: f})
	c := &hclient{tr: clientTrace{id: "ab.1"}}

	early := h.lockTimed() // a read returns and asks for the lock first
	h.mu.Unlock()
	time.Sleep(time.Millisecond)
	k := h.lockTimed() // input() writes the key
	h.mu.Unlock()
	k.key(c, 1)
	early.read(4096, 0) // the early read is recorded after the key
	time.Sleep(time.Millisecond)
	next := h.lockTimed()
	h.mu.Unlock()
	next.read(8, 0)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	recs := readTrace(t, f.Path())
	if len(recs) != 3 {
		t.Fatalf("records = %+v", recs)
	}
	key, first, second := recs[0], recs[1], recs[2]
	if first.Ev != "read" || first.Key != 0 || first.T >= key.T {
		t.Errorf("the read from before the key = %+v, key %+v", first, key)
	}
	if second.Ev != "read" || second.Key != 1 || second.Trace != "ab.1" || second.T < key.T {
		t.Errorf("the next read = %+v, want key 1 after %d", second, key.T)
	}
}

// With the trace off, the hooks take the lock and record nothing.
func TestLockTimesWithTheTraceOff(t *testing.T) {
	h := &Holder{}
	lt := h.lockTimed()
	h.mu.Unlock()
	if lt.tr != nil || lt.now() != 0 || lt.since(5) != 0 {
		t.Fatalf("lock times = %+v with the trace off", lt)
	}
	c := &hclient{}
	lt.key(c, 1)
	lt.read(1, 0)
	lt.tick()
	if c.tr.keys != 0 {
		t.Fatal("a key was counted with the trace off")
	}
}

// The trace runs while any traced client is attached, and a client that
// attaches again on one connection counts once.
func TestTraceRunsWhileATracedClientIsAttached(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fakeProc(t, map[string]string{"self/cgroup": "0::/system.slice/other.service\n"})
	h := &Holder{id: "trtest1", log: slog.New(slog.DiscardHandler)}
	if err := paths.EnsurePrivateDir(paths.SessionDir(h.id)); err != nil {
		t.Fatal(err)
	}
	a, b, plain := &hclient{}, &hclient{}, &hclient{}
	h.traceAttach(plain, "", nil)
	if h.dbg.tr.Load() != nil {
		t.Fatal("an attach without a trace ID started the trace")
	}
	h.traceAttach(a, "a.1", nil)
	h.traceAttach(a, "a.2", nil) // again, on the same connection
	h.traceAttach(b, "b.1", []string{diag.CatAttach, diag.CatNet})
	if len(h.dbg.traced) != 2 || h.dbg.tr.Load() == nil || a.tr.id != "a.2" {
		t.Fatalf("%d traced clients, trace %v, client a traces %q", len(h.dbg.traced), h.dbg.tr.Load(), a.tr.id)
	}
	// A client from part 1 names no categories: it gets attach and memory.
	// The host has no net category.
	if got := h.dbg.tr.Load().f.Cats(); got != partOneCats {
		t.Errorf("the trace records %s, want %s", got, partOneCats)
	}
	h.traceOff(a)
	h.traceOff(a) // a second detach changes nothing
	if h.dbg.tr.Load() == nil {
		t.Fatal("the trace stopped while client b is attached")
	}
	if got := h.dbg.tr.Load().f.Cats(); got.String() != diag.CatAttach {
		t.Errorf("with client b alone, the trace records %s", got)
	}
	h.traceOff(b)
	h.traceOff(plain)
	if len(h.dbg.traced) != 0 || h.dbg.tr.Load() != nil {
		t.Fatalf("after the last detach: %d traced clients, trace %v", len(h.dbg.traced), h.dbg.tr.Load())
	}
	var starts []string
	for _, r := range readTrace(t, paths.DebugTrace(h.id)) {
		if r.Ev == "start" && r.Trace != "" {
			starts = append(starts, r.Trace)
		}
	}
	if len(starts) != 3 || starts[0] != "a.1" || starts[1] != "a.2" || starts[2] != "b.1" {
		t.Fatalf("start records for %v", starts)
	}
}
