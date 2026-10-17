package diag

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestParseCats(t *testing.T) {
	c, err := ParseCats(" net, attach ,")
	if err != nil || c.String() != "attach,net" || !c.Has(CatNet) || c.Has(CatMemory) {
		t.Fatalf("cats = %v, %v", c, err)
	}
	if all, err := ParseCats(CatList); err != nil || all != AllCats || len(all.List()) != 5 {
		t.Fatalf("every category = %v, %v", all, err)
	}
	if _, err := ParseCats("net,disk"); err == nil || !strings.Contains(err.Error(), "no category disk") {
		t.Errorf("an unknown category: %v", err)
	}
	if _, err := ParseCats(""); !errors.Is(err, ErrNoCats) {
		t.Errorf("an empty list: %v", err)
	}
	// A newer peer may name a category this side does not know.
	if got := CatsOf([]string{"memory", "future"}); got.String() != "memory" {
		t.Errorf("CatsOf = %s", got)
	}
	if HostCats.String() != "attach,memory,runtime" || BridgeCats.String() != "net,runtime" {
		t.Errorf("host %s, bridge %s", HostCats, BridgeCats)
	}
}

// A file writes the records of its categories only, and every record about
// debug itself.
func TestAFileKeepsItsCategories(t *testing.T) {
	f, err := Open(filepath.Join(t.TempDir(), "d.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	f.SetCats(CatsOf([]string{CatNet}))
	f.Add(Record{T: 1, Cat: CatAttach, Ev: "key"})
	f.Add(Record{T: 2, Cat: CatNet, Ev: "ping"})
	f.Add(Record{T: 3, Cat: CatAttach, Ev: EvStart})
	f.Add(Record{T: 4, Ev: EvLog, Text: "about debug"})
	f.Add(Record{T: 5, Cat: CatRuntime, Ev: EvSave})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var evs []string
	for _, r := range readRecords(t, f.Path()) {
		evs = append(evs, r.Ev)
	}
	if strings.Join(evs, ",") != "ping,start,log,save" {
		t.Fatalf("events = %v", evs)
	}
}

// Debug lines go into the file as log records, in the text handler's form,
// with a secret hidden, and only in their category.
func TestDebugLines(t *testing.T) {
	lines := NewLines()
	log := slog.New(lines)
	if lines.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("lines with no file are enabled")
	}
	log.Error("before the file") // discarded
	f, err := Open(filepath.Join(t.TempDir(), "d.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	f.SetCats(CatsOf([]string{CatNet}))
	lines.SetFile(f)
	log.Debug("dialled", CatKey, CatNet, "host", "box", "secret", api.Secret("hunter2"))
	log.With(CatKey, CatAttach).Debug("resized", "cols", 80) // attach is off
	log.WithGroup("g").Warn("about debug", "n", 1)
	lines.SetFile(nil)
	log.Info("after the file") // discarded
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, f.Path())
	if len(recs) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	if r := recs[0]; r.Ev != EvLog || r.Cat != CatNet || r.Level != "DEBUG" || r.Text != `msg=dialled host=box secret=[redacted]` {
		t.Errorf("the first line = %+v", r)
	}
	if r := recs[1]; r.Cat != "" || r.Level != "WARN" || r.Text != `msg="about debug" g.n=1` {
		t.Errorf("the second line = %+v", r)
	}
}

// A run with the runtime category writes metrics each second, saves the
// flight recorder's window, at most once a minute by itself, and writes a
// CPU and a heap profile at its end. Every file is private.
func TestARunWithTheRuntimeCategory(t *testing.T) {
	dir := t.TempDir()
	run, err := StartRun(AllCats, RunOptions{
		Path:    filepath.Join(dir, "client-1.jsonl"),
		Start:   Record{Side: "client"},
		Runtime: RuntimeOptions{Dir: dir, Max: FolderMax, Prefix: "client-1-", CPU: true, Flight: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "a metrics record", func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "client-1.jsonl"))
		return bytes.Contains(b, []byte(`"ev":"metrics"`))
	})
	rt := run.Runtime()
	if rt == nil || !rt.Flight() {
		t.Fatal("no flight recorder with the runtime category on")
	}
	path, err := rt.Save("request")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(path), "client-1-flight-") || !strings.HasSuffix(path, ".trace") {
		t.Errorf("the window's name: %s", path)
	}
	rt.AutoSave("lock")
	rt.AutoSave("lock") // within a minute: no second save
	waitUntil(t, "the automatic save", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "client-1-"+AutoMark+"flight-*.trace"))
		return len(m) == 1 && !inUse(m[0])
	})
	run.Log.Info("about debug")
	if err := run.Stop("exit"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Save("request"); !errors.Is(err, ErrNoRecorder) {
		t.Errorf("a save after the stop: %v", err)
	}
	var saves, metricsSeen int
	recs := readRecords(t, filepath.Join(dir, "client-1.jsonl"))
	for _, r := range recs {
		switch r.Ev {
		case EvSave:
			saves++
		case "metrics":
			metricsSeen++
			if r.RT == nil || r.RT.HeapObjects <= 0 {
				t.Errorf("metrics = %+v", r.RT)
			}
		}
	}
	// The window by hand, one by itself, the CPU and the heap profiles.
	if saves != 4 || metricsSeen == 0 || recs[0].Ev != EvStart || recs[0].Cats != CatList || recs[len(recs)-1].Ev != EvStop {
		t.Fatalf("%d saves, %d metrics; records %+v", saves, metricsSeen, recs)
	}
	for _, name := range []string{"client-1-cpu.pprof", "client-1-heap.pprof"} {
		checkGzip(t, filepath.Join(dir, name))
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if st, _ := e.Info(); st.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %o", e.Name(), st.Mode().Perm())
		}
	}
}

// waitUntil polls cond until it holds, for 10 s at most.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func checkGzip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("%s is not gzipped: %v", path, err)
	}
	if b, err := io.ReadAll(z); err != nil || len(b) == 0 {
		t.Fatalf("%s: %d bytes, %v", path, len(b), err)
	}
}

func TestWriteProfile(t *testing.T) {
	dir := t.TempDir()
	for _, kind := range []string{ProfileCPU, ProfileHeap, ProfileGoroutine} {
		path, err := WriteProfile(context.Background(), kind, 50*time.Millisecond, dir)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(filepath.Base(path), kind+"-") {
			t.Errorf("the %s profile's name: %s", kind, path)
		}
		checkGzip(t, path)
	}
	if _, err := WriteProfile(context.Background(), "threads", time.Second, dir); err == nil {
		t.Error("an unknown profile was written")
	}
	// Two in one second get their own names.
	a, _ := WriteProfile(context.Background(), ProfileHeap, 0, dir)
	b, _ := WriteProfile(context.Background(), ProfileHeap, 0, dir)
	if a == b {
		t.Errorf("two profiles share the name %s", a)
	}
}

// A folder over its limit loses its oldest files first, but never a file in
// use, nor the list of files outside it.
func TestPrune(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i, name := range []string{"a.pprof", "b.pprof", OutsideList, "c.pprof", "d.pprof"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 100), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(time.Duration(i-10) * time.Minute)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest file is in use: a File has it open.
	f, err := Open(filepath.Join(dir, "a.pprof"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := Prune(dir, 300)
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != "a.pprof,d.pprof,"+OutsideList || n != 2 {
		t.Fatalf("removed %d, left %v", n, left)
	}
}

// writeAged writes files of 100 bytes in dir, each a minute newer than the
// one before it.
func writeAged(t *testing.T, dir string, names ...string) {
	t.Helper()
	now := time.Now()
	for i, name := range names {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 100), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(time.Duration(i-len(names)) * time.Minute)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func listDir(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// The windows that a process saved by itself go before the files made on
// request, and their own turnover keeps the newest KeepAuto of them and
// nothing else.
func TestAutomaticSavesNeverPushOutFilesMadeOnRequest(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, "heap-1.pprof", "auto-flight-1.trace", "auto-flight-2.trace", "flight-3.trace", "auto-flight-4.trace")
	if n := PruneAuto(dir, KeepAuto); n != 1 {
		t.Errorf("removed %d automatic saves, want 1", n)
	}
	if got := listDir(t, dir); got != "auto-flight-2.trace,auto-flight-4.trace,flight-3.trace,heap-1.pprof" {
		t.Fatalf("left %s", got)
	}
	// Over the folder's limit, the automatic saves go first, oldest first.
	if n := Prune(dir, 300); n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	if got := listDir(t, dir); got != "auto-flight-4.trace,flight-3.trace,heap-1.pprof" {
		t.Fatalf("left %s", got)
	}
}

// A file that another turnover removed first counts as removed, so a
// turnover removes no more than it must.
func TestAFileAlreadyGoneCountsAsRemoved(t *testing.T) {
	if !(folderFile{path: filepath.Join(t.TempDir(), "gone.pprof")}).remove() {
		t.Fatal("a file that is already gone does not count")
	}
}

// A file that a writer still writes stays through a turnover.
func TestAFileBeingWrittenStays(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, "old.pprof")
	f, err := CreatePrivate(filepath.Join(dir, "writing.pprof"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte("y"), 200)); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.Name(), old, old); err != nil {
		t.Fatal(err)
	}
	Prune(dir, 100)
	if got := listDir(t, dir); got != "writing.pprof" {
		t.Fatalf("left %s while one file is written", got)
	}
	f.Close()
	Prune(dir, 100)
	if got := listDir(t, dir); got != "" {
		t.Fatalf("left %s after the write", got)
	}
}

func TestHistogramDelta(t *testing.T) {
	s := []metrics.Sample{{Name: metricSched}}
	var prev []uint64
	metrics.Read(s)
	histogramDelta(s[0].Value, &prev)
	if len(prev) == 0 {
		t.Fatal("no counts kept")
	}
	if n, largest, p99 := histogramDelta(s[0].Value, &prev); n != 0 || largest != 0 || p99 != 0 {
		t.Errorf("no change gives %d, %d, %d", n, largest, p99)
	}
	h := &metrics.Float64Histogram{Counts: []uint64{0, 98, 2}, Buckets: []float64{0, 0.001, 0.010, math.Inf(1)}}
	var zero []uint64
	n, largest, p99 := histogramDeltaOf(h, &zero)
	if n != 100 || largest != int64(10*time.Millisecond) || p99 != int64(10*time.Millisecond) {
		t.Errorf("delta = %d, %d, %d", n, largest, p99)
	}
}
