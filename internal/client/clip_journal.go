package client

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The journal makes a clipboard write visible to every client process at
// once. When a reader sees a write, the memory appends its hashes to the
// journal, beside the shared file, in one O_APPEND write, before the output
// goes on to the terminal. It holds the lock shared while it appends, so
// appends never wait for each other. The goroutine that saves the memory
// folds the journal into the shared file, and empties it, with the lock
// held alone. A drop reads both, with the lock held alone, so it never
// reads half a record. Clipboard writes are rare, so normal output never
// touches the journal.
//
// Each record is one line: "c" for the clipboard, or "s" or "x" for the
// selection list of Ghostty's reading or xterm's, the SHA-256 in hex, and
// the time in Unix nanoseconds, which keeps the order of the writes. "u" and a time in Unix nanoseconds is a reader that
// lost track of a write: every drop is refused until then.

// journalWait is the longest that an append waits for the lock. An append
// that cannot write marks its reader orphaned, which refuses every drop.
const journalWait = 200 * time.Millisecond

func journalPath(path string) string { return path + ".journal" }

// journalKinds is the kind of record of each list: the clipboard's, then
// each reading's selection list.
const journalKinds = "csx"

// appendJournal appends records of writes to the journal, in their order,
// in one write: the first of each list at at, and each next one 1 ns later.
// A time unsure that is not zero adds the record of a reader that lost
// track of a write.
func appendJournal(path string, at time.Time, w *clipWrites) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(path), err)
	}
	var b []byte
	for k, sums := range w.lists() {
		for i, s := range sums {
			b = append(b, journalKinds[k], ' ')
			b = hex.AppendEncode(b, s[:])
			b = append(b, ' ')
			b = strconv.AppendInt(b, at.Add(time.Duration(i)).UnixNano(), 10)
			b = append(b, '\n')
		}
	}
	if !w.unsure.IsZero() {
		b = append(b, "u "...)
		b = strconv.AppendInt(b, w.unsure.UnixNano(), 10)
		b = append(b, '\n')
	}
	unlock, err := lockPath(path+".lock", journalWait, false)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.OpenFile(journalPath(path), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open the journal: %w", err)
	}
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("append to the journal: %w", err)
	}
	return nil
}

// openJournal makes sure that the journal can take a write: it opens it to
// append, and makes it when it is missing.
func openJournal(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(journalPath(path), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open the journal: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("open the journal: %w", err)
	}
	return nil
}

// readJournal adds the journal's records to have. The caller holds the lock
// alone. A last line with no end is a record that a process did not finish
// writing when it stopped, and counts as no record.
func readJournal(path string, have *clipSets) error {
	b, err := os.ReadFile(journalPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the journal: %w", err)
	}
	if i := bytes.LastIndexByte(b, '\n'); i < len(b)-1 {
		b = b[:i+1]
	}
	damaged := fmt.Errorf("%s is damaged", journalPath(path))
	for line := range bytes.Lines(b) {
		f := bytes.Fields(line)
		if len(f) == 2 && string(f[0]) == "u" {
			until, err := strconv.ParseInt(string(f[1]), 10, 64)
			if err != nil {
				return damaged
			}
			have.unsureUntil(time.Unix(0, until))
			continue
		}
		var s [32]byte
		n, herr := hex.Decode(s[:], fieldAt(f, 1))
		at, terr := strconv.ParseInt(string(fieldAt(f, 2)), 10, 64)
		if len(f) != 3 || len(f[0]) != 1 || herr != nil || n != len(s) || len(f[1]) != 2*len(s) || terr != nil {
			return damaged
		}
		k := strings.IndexByte(journalKinds, f[0][0])
		if k < 0 {
			return damaged
		}
		into := have.lists()[k]
		if t := time.Unix(0, at); t.After(into[s]) {
			into[s] = t
		}
	}
	return nil
}

func fieldAt(f [][]byte, i int) []byte {
	if i < len(f) {
		return f[i]
	}
	return nil
}
