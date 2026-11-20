package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
)

// The paste guard's memory (spec 7.6): the SHA-256 of the last 256
// clipboard writes of the last 24 hours, never their text, in one 0600 file
// that every client process on the laptop shares. A write that the reader
// sees goes to a queue in the process at once. A goroutine merges the queue
// into the file, so no file work runs on the output path. A drop checks the
// file and the queue. When the memory holds 256 writes, the guard refuses
// every drop until the oldest of them is a day old. A write that pushed an
// older one out is then a day old too, so the guard never forgets a write
// while it still lets a drop through.
//
// A write reaches the other processes at once through the journal
// (clip_journal.go), which the goroutine folds into the file.
//
// Writes to a selection, such as the primary one, leave the clipboard as it
// is. Each reading has a list of its own for them in the file, of the same
// size, which never pushes out a clipboard write and never fills the
// clipboard's list (clip_sets.go). A selection holds only its last write,
// so the oldest goes when a list is full.
//
// Each write has its own time, in nanoseconds, which keeps the order of
// the writes: the writes that one part of the output holds get times 1 ns
// apart, after every write that the process noted before. The lists keep
// the newest by that order, and keep all the writes that share the time of
// the last one kept, so no write goes for its hash.
//
// A reader that lost track of a write (clip_reader.go) shares the time
// until which every drop is refused, through the journal and then the file.
// So does a memory that sees a write more than clipSkew ahead of the clock:
// the clock moved back, and the order of the writes is not known.

const (
	clipKeep   = 256
	clipMaxAge = 24 * time.Hour
	clipSkew   = time.Minute // a time further ahead of the clock means that the clock moved back
	clipBatch  = 20 * time.Millisecond
	clipWait   = 2 * time.Second // the longest wait for the file's lock
)

// clipPath is the memory's file.
func clipPath() string { return filepath.Join(StateDir(), "paste-guard.json") }

// clipMemory is the memory in one file, as one process uses it.
type clipMemory struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	last    time.Time // the time of the last write that this process noted
	pending clipSets  // writes not yet in the file
	saving  bool      // a goroutine merges pending into the file
	fails   int
	terms   []*clipTerm // the terminal's state that the last attach left, for each reading

	loads atomic.Int64 // drops checked, for the tests
}

// clipLoadGap, when set, runs between the two reads of a check. Tests use
// it to merge the queue into the file just there.
var clipLoadGap func()

// clipPauseSave, when set, keeps the goroutine from saving. Tests use it to
// show that the journal alone makes a write visible.
var clipPauseSave bool

var (
	clipMemMu sync.Mutex
	clipMems  = map[string]*clipMemory{}
)

// clipMemoryAt is the memory in the file at path. Each process has one for
// each file, so every attach in the launcher shares its queue.
func clipMemoryAt(path string) *clipMemory {
	clipMemMu.Lock()
	defer clipMemMu.Unlock()
	m := clipMems[path]
	if m == nil {
		m = newClipMemory(path, time.Now)
		clipMems[path] = m
	}
	return m
}

func newClipMemory(path string, now func() time.Time) *clipMemory {
	return &clipMemory{path: path, now: now, pending: newClipSets()}
}

// takeTerms returns the terminal's state of each reading that the last
// attach in this process left, for the next one, or none.
func (m *clipMemory) takeTerms() []*clipTerm {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.terms
	m.terms = nil
	return t
}

// keepTerms keeps the terminal's state of each reading for the next attach
// in this process.
func (m *clipMemory) keepTerms(t []*clipTerm) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terms = t
}

// add remembers writes, to the clipboard or, with sel, to the selection
// list of Ghostty's reading.
func (m *clipMemory) add(sel bool, sums ...[32]byte) error {
	var w clipWrites
	w.add(0, sel, sums...)
	return m.addBatch(&w)
}

// addBatch remembers the writes of one part of the output, in their order.
// It appends them to the journal, in one write, before it returns, so every
// client process sees them at once, and queues them for the file. An error
// means that the journal could not take them.
//
// The times go on from the last write of the process, unless that is more
// than clipSkew ahead of the clock: the clock moved back, and times ahead
// of it would make the newest writes look old.
func (m *clipMemory) addBatch(w *clipWrites) error {
	if w.empty() {
		return nil
	}
	m.mu.Lock()
	at := m.now().Round(0)
	if m.last.After(at.Add(clipSkew)) {
		m.last = time.Time{}
	}
	if !at.After(m.last) {
		at = m.last.Add(1)
	}
	m.last = at.Add(time.Duration(w.size() - 1))
	m.mu.Unlock()
	err := appendJournal(m.path, at, w)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending.addWrites(w, at)
	if !m.saving && !clipPauseSave {
		m.saving, m.fails = true, 0
		go m.save()
	}
	return err
}

// save merges the queue into the file until the queue is empty. After three
// failures in a row it stops, and the queue waits for the next write or flush.
func (m *clipMemory) save() {
	for {
		time.Sleep(clipBatch)
		m.mu.Lock()
		batch := m.pending.clone()
		m.mu.Unlock()
		err := m.merge(batch)
		m.mu.Lock()
		if err == nil {
			m.fails = 0
			m.pending.forgetSaved(batch)
		} else {
			m.fails++
			DebugLog().Debug("paste guard cannot save", diag.CatKey, diag.CatAttach, "err", err)
		}
		if m.pending.empty() || m.fails >= 3 {
			m.saving = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

// flush waits, up to wait, until the queue is in the file. An attach calls
// it at its end, so a write that a session made just before the client
// exits reaches the other client processes.
func (m *clipMemory) flush(wait time.Duration) {
	deadline := time.Now().Add(wait)
	for {
		m.mu.Lock()
		empty, saving := m.pending.empty(), m.saving
		if !empty && !saving && !clipPauseSave {
			m.saving, m.fails = true, 0
			go m.save()
		}
		m.mu.Unlock()
		if empty && !saving || time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// merge adds a batch of writes, and the journal, to the file, and empties
// the journal.
func (m *clipMemory) merge(batch clipSets) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(m.path), err)
	}
	unlock, err := lockPath(m.path+".lock", clipWait, true)
	if err != nil {
		return err
	}
	defer unlock()
	have, err := readClipFile(m.path)
	if err == nil {
		err = readJournal(m.path, &have)
	}
	if err != nil {
		return err // a file that cannot be read stays as it is
	}
	have.join(batch)
	have.prune(m.now())
	have.keepNewest()
	if err := writeClipFile(m.path, have); err != nil {
		return err
	}
	if err := os.Truncate(journalPath(m.path), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("empty the journal: %w", err)
	}
	return nil
}

// check says why a paste must not upload, or "" if it may. sums are the
// hashes of the paste's text and of each of its paths. extra are the hashes
// of writes that have not ended. match is the index in sums of the hash that
// a session wrote, or -1.
func (m *clipMemory) check(sums, extra [][32]byte) (why string, match int) {
	have, err := m.load()
	if err != nil {
		return "could not be checked, because the client cannot read its memory of clipboard writes (" + err.Error() + ")", -1
	}
	for i, s := range sums {
		if have.has(s) || slices.Contains(extra, s) {
			return "is a path that a session put on your clipboard in the last day", i
		}
	}
	if !have.unsure.IsZero() {
		return "may be a path that a session put on your clipboard: a client lost track of a clipboard write, " +
			"or the clock moved back, so every drop is refused until " + have.unsure.Local().Format("15:04 on 2 Jan"), -1
	}
	if len(have.board) >= clipKeep {
		var oldest time.Time
		for _, at := range have.board {
			if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
		}
		return fmt.Sprintf("may be a path that a session put on your clipboard: the client remembers %d writes "+
			"from the last day, its limit, so it refuses every drop until %s", clipKeep,
			oldest.Add(clipMaxAge).Local().Format("15:04 on 2 Jan")), -1
	}
	return "", -1
}

// load reads the queue, then the file and the journal, without writes older
// than a day. It
// copies the queue first, without file work under the lock: a write that the
// goroutine moves from the queue to the file meanwhile is then in the copy,
// or in the file.
func (m *clipMemory) load() (clipSets, error) {
	m.loads.Add(1)
	m.mu.Lock()
	have := m.pending.clone()
	m.mu.Unlock()
	if clipLoadGap != nil {
		clipLoadGap()
	}
	// A memory whose journal cannot take a write would miss writes that
	// other processes saw: the check fails closed.
	if err := openJournal(m.path); err != nil {
		return clipSets{}, err
	}
	if _, err := os.Stat(filepath.Dir(m.path)); err == nil {
		unlock, err := lockPath(m.path+".lock", clipWait, true)
		if err != nil {
			return clipSets{}, err
		}
		file, err := readClipFile(m.path)
		if err == nil {
			err = readJournal(m.path, &file)
		}
		unlock()
		if err != nil {
			return clipSets{}, err
		}
		have.join(file)
	} else if !errors.Is(err, os.ErrNotExist) {
		return clipSets{}, fmt.Errorf("read %s: %w", filepath.Dir(m.path), err)
	}
	have.prune(m.now())
	return have, nil
}
