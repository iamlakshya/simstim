package diag

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordFormat(t *testing.T) {
	for _, c := range []struct {
		r    Record
		want string
	}{
		{Record{T: 5, Cat: CatAttach, Ev: "key", Trace: "ab.1", Key: 3, Read: 2, N: 1, Write: 40},
			`{"t":5,"cat":"attach","ev":"key","trace":"ab.1","key":3,"read":2,"n":1,"write":40}`},
		{Record{T: 9, Cat: CatNet, Ev: "ping", Ping: 4, RTT: 100, Host: 7, Offset: -3},
			`{"t":9,"cat":"net","ev":"ping","ping":4,"rtt":100,"host":7,"offset":-3}`},
		{Record{T: 1, Cat: CatMemory, Ev: "sample", Mem: &Memory{Current: 10, Anon: 6, File: 4}},
			`{"t":1,"cat":"memory","ev":"sample","mem":{"current":10,"anon":6,"file":4,"workingset_refault_file":0,"pgmajfault":0,"high":0,"max":0,"oom":0,"oom_kill":0,"mem_some":0,"mem_full":0,"cpu_some":0,"cpu_full":0}}`},
	} {
		b, err := json.Marshal(c.r)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("record = %s\nwant      %s", b, c.want)
		}
	}
}

// readRecords reads a trace file's lines, each of which must be a record.
func readRecords(t *testing.T, path string) []Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("line %q is not a record: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

func TestFileWritesRecordsAsPrivateJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	// An old file is made private too, and new records follow its lines.
	if err := os.WriteFile(path, []byte(`{"t":1,"cat":"attach","ev":"old"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		f.Add(Record{T: int64(10 + i), Cat: CatAttach, Ev: "key", Key: int64(i + 1)})
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	f.Add(Record{T: 99, Cat: CatAttach, Ev: "late"}) // after Close: not written, and no panic
	recs := readRecords(t, path)
	if len(recs) != 4 || recs[0].Ev != "old" || recs[3].Key != 3 {
		t.Fatalf("records = %+v", recs)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
}

func TestFileTurnsOverAtItsLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	const max = 400
	f, err := Open(path, max)
	if err != nil {
		t.Fatal(err)
	}
	const n = 40
	for i := range n {
		f.Add(Record{T: int64(i + 1), Cat: CatAttach, Ev: "read", N: 4096})
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cur, old := readRecords(t, path), readRecords(t, path+".1")
	if len(cur) == 0 || len(old) == 0 {
		t.Fatalf("%d records in the file and %d in the old one, want some in both", len(cur), len(old))
	}
	// The newest records are in the file, the ones before them in the old one.
	if cur[len(cur)-1].T != n || old[len(old)-1].T != cur[0].T-1 {
		t.Errorf("the file ends at %d and starts at %d, the old one ends at %d", cur[len(cur)-1].T, cur[0].T, old[len(old)-1].T)
	}
	for _, p := range []string{path, path + ".1"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > max || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %d bytes, mode %o; want at most %d bytes, mode 600", p, st.Size(), st.Mode().Perm(), max)
		}
	}
}

// Add never blocks: a full queue drops records, and the file says how many.
func TestFileDropsRecordsWhenTheQueueIsFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	file, size, err := openAppend(path)
	if err != nil {
		t.Fatal(err)
	}
	// No writer runs yet, so the queue fills.
	f := &File{path: path, max: DefaultMax, q: make(chan Record, 2), stop: make(chan struct{}), done: make(chan struct{})}
	for i := range 5 {
		f.Add(Record{T: int64(i + 1), Cat: CatAttach, Ev: "key"})
	}
	if got := f.dropped.Load(); got != 3 {
		t.Fatalf("dropped = %d, want 3", got)
	}
	go f.run(file, size)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, path)
	if len(recs) != 3 || recs[2].Ev != "dropped" || recs[2].Dropped != 3 {
		t.Fatalf("records = %+v, want 2 and a count of 3 dropped", recs)
	}
}

func TestNilFileWritesNothing(t *testing.T) {
	var f *File
	f.Add(Record{Ev: "x"})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailsInAMissingFolder(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "no", "such", "trace.jsonl"), 0)
	if err == nil || !strings.Contains(err.Error(), "open the trace file") {
		t.Fatalf("err = %v", err)
	}
}

func TestNowIsMonotonic(t *testing.T) {
	a := Now()
	b := Now()
	if a <= 0 || b < a {
		t.Fatalf("Now went from %d to %d", a, b)
	}
}

// Now follows the system's monotonic clock, which every process on a host
// shares: the bridge and the holder write comparable times.
func TestNowFollowsTheSystemClock(t *testing.T) {
	sys := sysMono()
	if sys == 0 {
		t.Skip("no system monotonic clock here")
	}
	if d := Now() - sys; d < -int64(10*time.Millisecond) || d > int64(10*time.Millisecond) {
		t.Fatalf("Now is %v away from CLOCK_MONOTONIC", time.Duration(d))
	}
}
