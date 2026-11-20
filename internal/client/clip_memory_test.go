package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testMemory is a memory in a new file, with a clock that the test moves.
// Another process that shares the file is another clipMemory on the path.
func testMemory(t *testing.T) (*clipMemory, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	m := newClipMemory(filepath.Join(t.TempDir(), "state", "paste-guard.json"), clock.now)
	// The goroutine that saves the memory ends before the test removes its
	// folder.
	t.Cleanup(func() { m.flush(5 * time.Second) })
	return m, clock
}

func otherProcess(m *clipMemory) *clipMemory {
	return newClipMemory(m.path, m.now)
}

func sumOf(t *testing.T, s string) [32]byte {
	t.Helper()
	k, ok := keySum(s)
	if !ok {
		t.Fatalf("%q has no key", s)
	}
	return k
}

// One process records a write; another refuses a drop of its path, as after
// a restart. The file keeps hashes only, with mode 0600.
func TestClipMemorySharedFile(t *testing.T) {
	m, _ := testMemory(t)
	must0(t, m.add(false, sumOf(t, victim)))
	m.flush(5 * time.Second)
	other := otherProcess(m)
	if why, match := other.check([][32]byte{sumOf(t, victim)}, nil); !strings.Contains(why, "put on your clipboard") || match != 0 {
		t.Fatalf("another process: %q, %d", why, match)
	}
	if why, _ := other.check([][32]byte{sumOf(t, "/Users/me/other.pdf")}, nil); why != "" {
		t.Fatalf("another path: %q", why)
	}
	b, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "taxes") {
		t.Errorf("the file holds text: %s", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(m.path); st.Mode().Perm() != 0o600 {
			t.Errorf("the file's mode is %v", st.Mode().Perm())
		}
		if st, _ := os.Stat(filepath.Dir(m.path)); st.Mode().Perm() != 0o700 {
			t.Errorf("the folder's mode is %v", st.Mode().Perm())
		}
	}
}

// A drop checks the queue too, before the file has the write.
func TestClipMemoryQueue(t *testing.T) {
	m, _ := testMemory(t)
	m.mu.Lock()
	m.pending.board[sumOf(t, victim)] = m.now()
	m.mu.Unlock()
	if why, _ := m.check([][32]byte{sumOf(t, victim)}, nil); why == "" {
		t.Fatal("a queued write was not checked")
	}
	if why, _ := m.check(nil, nil); why != "" {
		t.Fatalf("no paths: %q", why)
	}
	if why, _ := m.check([][32]byte{sumOf(t, "/a.png")}, [][32]byte{sumOf(t, "/a.png")}); why == "" {
		t.Fatal("a write that has not ended was not checked")
	}
}

// V2: the goroutine moves a write from the queue to the file between the
// two reads of a check. The check still finds it, in one read or the other.
func TestClipMemoryQueueMovesDuringCheck(t *testing.T) {
	m, _ := testMemory(t)
	m.mu.Lock()
	m.pending.board[sumOf(t, victim)] = m.now()
	m.mu.Unlock()
	clipLoadGap = func() {
		m.mu.Lock()
		batch := m.pending.clone()
		m.mu.Unlock()
		if err := m.merge(batch); err != nil {
			t.Error(err)
		}
		m.mu.Lock()
		m.pending.forgetSaved(batch)
		m.mu.Unlock()
	}
	defer func() { clipLoadGap = nil }()
	if why, _ := m.check([][32]byte{sumOf(t, victim)}, nil); why == "" {
		t.Fatal("a write that moved from the queue to the file was missed")
	}
}

// V6: writes to a selection have a list of their own. They push out no
// clipboard write, and never fill the clipboard's list.
func TestClipMemorySelectionsApart(t *testing.T) {
	m, _ := testMemory(t)
	must0(t, m.add(false, sumOf(t, victim)))
	for i := range clipKeep + 50 {
		must0(t, m.add(true, sumOf(t, fmt.Sprintf("/sel/%d", i))))
	}
	m.flush(10 * time.Second)
	have, err := readClipFile(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(have.board) != 1 || len(have.sel[0]) != clipKeep {
		t.Fatalf("the file holds %d clipboard and %d selection writes", len(have.board), len(have.sel[0]))
	}
	if why, _ := m.check([][32]byte{sumOf(t, victim)}, nil); why == "" {
		t.Fatal("the clipboard write was pushed out")
	}
	if why, _ := m.check([][32]byte{sumOf(t, "/a.png")}, nil); why != "" {
		t.Fatalf("selection writes filled the memory: %q", why)
	}
	if why, _ := m.check([][32]byte{sumOf(t, fmt.Sprintf("/sel/%d", clipKeep+49))}, nil); why == "" {
		t.Fatal("a selection write was not checked")
	}
}

// Two processes write at once, and neither loses a write.
func TestClipMemoryWritersAtOnce(t *testing.T) {
	m, _ := testMemory(t)
	other := otherProcess(m)
	var wg sync.WaitGroup
	for i, mem := range []*clipMemory{m, other} {
		wg.Go(func() {
			for j := range 50 {
				if err := mem.add(false, sumOf(t, fmt.Sprintf("/w%d/%d", i, j))); err != nil {
					t.Error(err)
				}
				time.Sleep(time.Millisecond)
			}
			mem.flush(10 * time.Second)
		})
	}
	wg.Wait()
	have, err := readClipFile(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(have.board) != 100 {
		t.Fatalf("the file holds %d writes, want 100", len(have.board))
	}
}

// A file that the client cannot read refuses every drop, and stays as it is.
func TestClipMemoryDamagedFile(t *testing.T) {
	m, _ := testMemory(t)
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if why, _ := m.check([][32]byte{sumOf(t, "/a.png")}, nil); !strings.Contains(why, "cannot read") {
		t.Fatalf("a damaged file: %q", why)
	}
	batch := newClipSets()
	batch.board[sumOf(t, victim)] = m.now()
	if err := m.merge(batch); err == nil {
		t.Fatal("a merge wrote over a damaged file")
	}
	if b, _ := os.ReadFile(m.path); string(b) != "{not json" {
		t.Fatalf("the damaged file changed: %q", b)
	}
	// So does a lock that another process holds for the whole wait.
	m2, _ := testMemory(t)
	if err := os.MkdirAll(filepath.Dir(m2.path), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockPath(m2.path+".lock", time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if why, _ := m2.check([][32]byte{sumOf(t, "/a.png")}, nil); !strings.Contains(why, "cannot read") {
		t.Fatalf("a held lock: %q", why)
	}
}

// Writes are kept for a day. A time ahead of the clock counts as now.
func TestClipMemoryAge(t *testing.T) {
	m, clock := testMemory(t)
	must0(t, m.add(false, sumOf(t, victim)))
	m.flush(5 * time.Second)
	clock.add(clipMaxAge)
	if why, _ := m.check([][32]byte{sumOf(t, victim)}, nil); why == "" {
		t.Fatal("forgotten before a day")
	}
	clock.add(time.Second)
	if why, _ := m.check([][32]byte{sumOf(t, victim)}, nil); why != "" {
		t.Fatalf("after a day: %q", why)
	}
	have := clipSums{sumOf(t, victim): clock.now().Add(48 * time.Hour)}
	pruneSums(have, clock.now())
	if !have[sumOf(t, victim)].Equal(clock.now()) {
		t.Fatalf("a time ahead of the clock became %v", have[sumOf(t, victim)])
	}
}

// R3: when the memory is full, every drop is refused until the oldest
// write is a day old. A write that pushed an older one out is a day old by
// then, so no write is forgotten while a drop goes through.
func TestClipMemoryFull(t *testing.T) {
	m, clock := testMemory(t)
	start := clock.now()
	must0(t, m.add(false, sumOf(t, victim))) // the first write, at start
	for i := range clipKeep {
		clock.add(time.Minute)
		must0(t, m.add(false, sumOf(t, fmt.Sprintf("/other/%d", i))))
	}
	m.flush(10 * time.Second)
	have, err := readClipFile(m.path)
	if err != nil || len(have.board) != clipKeep {
		t.Fatalf("the file holds %d writes, %v", len(have.board), err)
	}
	if _, ok := have.board[sumOf(t, victim)]; ok {
		t.Fatal("the oldest write was not the one pushed out")
	}
	for _, at := range []time.Time{clock.now(), start.Add(clipMaxAge + time.Minute)} {
		clock.set(at)
		if why, _ := m.check([][32]byte{sumOf(t, "/a.png")}, nil); !strings.Contains(why, "refuses every drop until") {
			t.Fatalf("at %v, a full memory: %q", at, why)
		}
	}
	// The oldest kept write is a day old: the one pushed out is older still.
	clock.set(start.Add(clipMaxAge + time.Minute + time.Second))
	if why, _ := m.check([][32]byte{sumOf(t, "/a.png")}, nil); why != "" {
		t.Fatalf("after the oldest aged out: %q", why)
	}
	if start.Add(clipMaxAge).After(clock.now()) {
		t.Fatal("the write that was pushed out is not a day old yet")
	}
}

// The lock lets one holder in at a time, across open files.
func TestLockPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := lockPath(p, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, exclusive := range []bool{true, false} {
		if _, err := lockPath(p, 50*time.Millisecond, exclusive); !errors.Is(err, errLockBusy) {
			t.Fatalf("a second lock, exclusive %v: %v", exclusive, err)
		}
	}
	unlock()
	// Shared holders share, and keep an exclusive one out.
	s1, err := lockPath(p, time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := lockPath(p, time.Second, false)
	if err != nil {
		t.Fatalf("a second shared lock: %v", err)
	}
	if _, err := lockPath(p, 50*time.Millisecond, true); !errors.Is(err, errLockBusy) {
		t.Fatalf("an exclusive lock beside shared ones: %v", err)
	}
	s1()
	s2()
	unlock2, err := lockPath(p, time.Second, true)
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	unlock2()
}

// W5: a write is in the journal before the reader's output goes on, so
// another process sees it while the goroutine has not saved it.
func TestClipMemoryJournalBeforeSave(t *testing.T) {
	clipPauseSave = true
	defer func() { clipPauseSave = false }()
	m, _ := testMemory(t)
	if err := m.add(false, sumOf(t, victim)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.path); err == nil {
		t.Fatal("the paused goroutine saved the file")
	}
	other := otherProcess(m)
	if why, _ := other.check([][32]byte{sumOf(t, victim)}, nil); why == "" {
		t.Fatal("another process missed a write that is only in the journal")
	}
	// The goroutine folds the journal into the file, and empties it.
	clipPauseSave = false
	m.flush(5 * time.Second)
	if b, _ := os.ReadFile(journalPath(m.path)); len(b) != 0 {
		t.Errorf("the journal still holds %q", b)
	}
	if why, _ := other.check([][32]byte{sumOf(t, victim)}, nil); why == "" {
		t.Fatal("the write was lost when the journal was folded")
	}
}

// W5: when the journal cannot take a write, the reader lost track of it,
// and every drop is refused for a day.
func TestClipMemoryJournalFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Windows and root do not refuse a write to a folder of mode 0500")
	}
	m, _ := testMemory(t)
	dir := filepath.Dir(m.path)
	must0(t, os.MkdirAll(dir, 0o700))
	must0(t, os.Chmod(dir, 0o500))
	defer func() { _ = os.Chmod(dir, 0o700) }()
	r := newClipReader(m.add, m.now, nil)
	r.observe([]byte(osc52(victim)))
	if !r.orphaned() {
		t.Fatal("a write that the journal could not take did not orphan the reader")
	}
}

// V5: a check fails closed when the journal cannot take a write, as in a
// state folder that the user cannot write.
func TestClipMemoryCheckNeedsTheJournal(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Windows and root do not refuse a write to a folder of mode 0500")
	}
	m, _ := testMemory(t)
	dir := filepath.Dir(m.path)
	must0(t, os.MkdirAll(dir, 0o700))
	// The lock and the shared file are there, and can be read. Only the
	// journal is missing, and the folder takes no new file.
	unlock, err := lockPath(m.path+".lock", time.Second, true)
	must0(t, err)
	unlock()
	must0(t, os.WriteFile(m.path, []byte(`{"writes":[]}`), 0o600))
	must0(t, os.Chmod(dir, 0o500))
	defer func() { _ = os.Chmod(dir, 0o700) }()
	if why, _ := otherProcess(m).check([][32]byte{sumOf(t, "/a.png")}, nil); !strings.Contains(why, "cannot read") {
		t.Fatalf("a check with no journal that can take a write: %q", why)
	}
}

// V7: a batch of many writes goes in with one prune.
func TestClipMemoryLargeBatch(t *testing.T) {
	clipPauseSave = true
	defer func() { clipPauseSave = false }()
	m, _ := testMemory(t)
	batch := make([][32]byte, 100_000)
	for i := range batch {
		batch[i] = sumOf(t, strconv.Itoa(i))
	}
	start, sorts := time.Now(), keepSorts.Load()
	must0(t, m.addBatch(&clipWrites{board: batch}))
	if d := time.Since(start); d > max(stressLimit, 3*time.Second) {
		t.Errorf("a batch of 100,000 writes took %v", d)
	}
	// Timing alone is not sure on a busy machine: count the sorts too.
	if n := keepSorts.Load() - sorts; n > 10 {
		t.Errorf("a batch of 100,000 writes pruned its list %d times", n)
	}
	if n := len(m.pending.board); n != clipKeep {
		t.Errorf("the queue keeps %d writes, want %d", n, clipKeep)
	}
}

// V3, round 5: the lists keep the order of the writes, never that of their
// hashes. 300 writes to the primary selection and then a target, in one
// part of the output, keep the target, which the selection holds now. So
// does the next process that saves the file.
func TestClipMemoryKeepsWriteOrder(t *testing.T) {
	for i := range 8 {
		target := fmt.Sprintf("/Users/me/target-%d.pdf", i)
		m, _ := testMemory(t)
		g := newPasteGuard(m)
		var out []byte
		for j := range 300 {
			out = append(out, "\x1b]52;p;"+b64(fmt.Sprintf("/tmp/primary/%d", j))+"\a"...)
		}
		g.observe(append(out, "\x1b]52;p;"+b64(target)+"\a"...))
		m.flush(5 * time.Second)
		if why, _ := otherProcess(m).check([][32]byte{sumOf(t, target)}, nil); why == "" {
			t.Fatalf("%s: the newest write to the primary selection was pushed out", target)
		}
		other := otherProcess(m)
		must0(t, other.add(true, sumOf(t, "/tmp/primary/next")))
		other.flush(5 * time.Second)
		if why, _ := otherProcess(m).check([][32]byte{sumOf(t, target)}, nil); why == "" {
			t.Fatalf("%s: the next process that saved the file pushed it out", target)
		}
	}
}

// V4, round 5: a reader that lost track of a write shares it. Another
// process refuses every drop, from the journal, and so does a process
// that starts after the journal was folded into the file, until a day has
// passed.
func TestClipMemoryUnsureIsShared(t *testing.T) {
	clipPauseSave = true
	defer func() { clipPauseSave = false }()
	m, clock := testMemory(t)
	newPasteGuard(m).observe([]byte("\x1b]52;" + strings.Repeat("c", 17) + ";QUFB\a"))
	png := [][32]byte{sumOf(t, "/Users/me/shot.png")}
	if why, _ := otherProcess(m).check(png, nil); !strings.Contains(why, "lost track") {
		t.Fatalf("another process, from the journal: %q", why)
	}
	clipPauseSave = false
	m.flush(5 * time.Second)
	if b, _ := os.ReadFile(journalPath(m.path)); len(b) != 0 {
		t.Fatalf("the journal still holds %q", b)
	}
	if why, _ := otherProcess(m).check(png, nil); !strings.Contains(why, "lost track") {
		t.Fatalf("a process that starts later, from the file: %q", why)
	}
	clock.add(clipMaxAge + time.Minute)
	if why, _ := otherProcess(m).check(png, nil); why != "" {
		t.Fatalf("a day later: %q", why)
	}
}

// V4, round 5: a kitty transfer that the reader lost track of is shared
// again when it ends, so other processes refuse for a day after the commit.
func TestClipMemoryUnsureKittyEnd(t *testing.T) {
	m, clock := testMemory(t)
	g := newPasteGuard(m)
	g.observe([]byte("\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;QUFB\x1b\\")) // data with no start
	png := [][32]byte{sumOf(t, "/Users/me/shot.png")}
	// F2, round 6: shared at once, before any commit, in case this process
	// stops before a guarded client sees the commit.
	if why, _ := otherProcess(m).check(png, nil); !strings.Contains(why, "lost track") {
		t.Fatalf("another process, before the commit: %q", why)
	}
	clock.add(25 * time.Hour)
	g.observe([]byte("\x1b]5522;type=wdata\x1b\\")) // the commit
	clock.add(23 * time.Hour)
	if why, _ := otherProcess(m).check(png, nil); !strings.Contains(why, "lost track") {
		t.Fatalf("another process, 23 hours after the commit: %q", why)
	}
	clock.add(2 * time.Hour)
	if why, _ := otherProcess(m).check(png, nil); why != "" {
		t.Fatalf("another process, a day after the commit: %q", why)
	}
}

// F1, round 6: each reading has its own selection list, so the writes of
// one reading never push out the newest write of the other, which that
// reading's primary selection holds. ghosttyOnly is a write that only
// Ghostty's reading sees, and xtermOnly W1's form, which only xterm's sees.
func TestClipMemorySelectionPerReading(t *testing.T) {
	ghosttyOnly := func(s string) string { return "\x1b\u00dd52;p;" + b64(s) + "\a" }
	xtermOnly := func(s string) string { return "\x1b\u00dd]52;p;" + b64(s) + "\a" }
	plain := func(s string) string { return "\x1b]52;p;" + b64(s) + "\a" }
	many := func(form func(string) string) string {
		var b strings.Builder
		for i := range 300 {
			b.WriteString(form(fmt.Sprintf("/tmp/primary/%d", i)))
		}
		return b.String()
	}
	const target = "/Users/me/target.pdf"
	for _, c := range []struct {
		name  string
		parts []string
		procs []int // the process of each part
	}{
		{"300 writes, then the target that Ghostty alone sees", []string{many(plain) + ghosttyOnly(target)}, []int{0}},
		{"the target that Ghostty alone sees, then 300 that xterm alone sees", []string{ghosttyOnly(target) + many(xtermOnly)}, []int{0}},
		{"the same in two parts", []string{ghosttyOnly(target), many(xtermOnly)}, []int{0, 0}},
		{"the same in two processes", []string{ghosttyOnly(target), many(xtermOnly)}, []int{0, 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, clock := testMemory(t)
			mems := []*clipMemory{m, otherProcess(m)}
			guards := []*pasteGuard{newPasteGuard(mems[0]), newPasteGuard(mems[1])}
			for i, part := range c.parts {
				clock.add(time.Millisecond)
				guards[c.procs[i]].observe([]byte(part))
				mems[c.procs[i]].flush(5 * time.Second)
			}
			for _, s := range []string{target, "/tmp/primary/299"} {
				if why, match := otherProcess(m).check([][32]byte{sumOf(t, s)}, nil); match != 0 {
					t.Errorf("%s was pushed out: %q", s, why)
				}
			}
		})
	}
}

// F3, round 6: a clock that moves back makes old writes look newer than
// new ones. The memory then refuses every drop for a day, in every process.
func TestClipMemoryClockMovesBack(t *testing.T) {
	plain := func(s string) string { return "\x1b]52;p;" + b64(s) + "\a" }
	m, clock := testMemory(t)
	var out strings.Builder
	for i := range 300 {
		out.WriteString(plain(fmt.Sprintf("/tmp/primary/%d", i)))
	}
	newPasteGuard(m).observe([]byte(out.String()))
	m.flush(5 * time.Second)
	clock.add(-10 * time.Minute)
	other := otherProcess(m)
	newPasteGuard(other).observe([]byte(plain("/Users/me/target.pdf")))
	clock.add(time.Second) // the goroutine saves a little later
	other.flush(5 * time.Second)
	if why, _ := otherProcess(m).check([][32]byte{sumOf(t, "/Users/me/target.pdf")}, nil); why == "" {
		t.Fatal("after the clock moved back, the newest write to the primary selection was pushed out, and a drop passed")
	}
}

// F3, round 6: a process whose clock moved back an hour, after a write,
// gives its next writes times from the clock, so one part of the output
// cannot grow the file past the limits of its lists.
func TestClipMemoryClockBackKeepsTheFileSmall(t *testing.T) {
	m, clock := testMemory(t)
	must0(t, m.add(true, sumOf(t, "/tmp/first")))
	clock.add(-time.Hour)
	var out strings.Builder
	for i := range 20_000 {
		out.WriteString("\x1b]52;p;" + b64(fmt.Sprintf("/tmp/primary/%d", i)) + "\a")
	}
	newPasteGuard(m).observe([]byte(out.String()))
	m.flush(10 * time.Second)
	have, err := readClipFile(m.path)
	must0(t, err)
	for i, l := range have.lists() {
		if len(l) > clipKeep {
			t.Errorf("list %d of the file holds %d writes", i, len(l))
		}
	}
	if have.unsure.IsZero() {
		t.Error("the write from before the clock moved back did not make the memory unsure")
	}
}
