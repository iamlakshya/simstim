package client

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// The paste guard's file (clip_memory.go): JSON, with the hash of each
// write and its time in nanoseconds, oldest first.

type clipFile struct {
	Writes     []clipEntry `json:"writes"`
	Selections []clipEntry `json:"selections,omitempty"` // Ghostty's reading
	// SelectionsXterm is the selection list of xterm's reading.
	SelectionsXterm []clipEntry `json:"selections_xterm,omitempty"`
	// UnsureUntil is the time, in Unix nanoseconds, until which every drop
	// is refused, because a reader lost track of a write.
	UnsureUntil int64 `json:"unsure_until_ns,omitempty"`
}

type clipEntry struct {
	SHA256 string `json:"sha256"`
	At     int64  `json:"at_ns"` // Unix nanoseconds, from the wall clock: the order of the writes
}

func readClipFile(path string) (clipSets, error) {
	have := newClipSets()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return have, nil
	}
	if err != nil {
		return clipSets{}, fmt.Errorf("read %s: %w", path, err)
	}
	var f clipFile
	if err := json.Unmarshal(b, &f); err != nil {
		return clipSets{}, fmt.Errorf("%s is damaged: %w", path, err)
	}
	for _, part := range []struct {
		entries []clipEntry
		into    clipSums
	}{{f.Writes, have.board}, {f.Selections, have.sel[0]}, {f.SelectionsXterm, have.sel[1]}} {
		for _, e := range part.entries {
			var s [32]byte
			if n, err := hex.Decode(s[:], []byte(e.SHA256)); err != nil || n != len(s) || len(e.SHA256) != 2*len(s) {
				return clipSets{}, fmt.Errorf("%s is damaged: a hash is not SHA-256", path)
			}
			part.into[s] = time.Unix(0, e.At)
		}
	}
	if f.UnsureUntil != 0 {
		have.unsure = time.Unix(0, f.UnsureUntil)
	}
	return have, nil
}

func clipEntries(have clipSums) []clipEntry {
	out := make([]clipEntry, 0, len(have))
	for s, at := range have {
		out = append(out, clipEntry{SHA256: hex.EncodeToString(s[:]), At: at.UnixNano()})
	}
	slices.SortFunc(out, func(a, b clipEntry) int {
		if c := cmp.Compare(a.At, b.At); c != 0 {
			return c
		}
		return cmp.Compare(a.SHA256, b.SHA256)
	})
	return out
}

// writeClipFile writes the memory to a new file, and renames it over the
// old one, so a reader never sees half a file.
func writeClipFile(path string, have clipSets) error {
	f := clipFile{Writes: clipEntries(have.board), Selections: clipEntries(have.sel[0]),
		SelectionsXterm: clipEntries(have.sel[1])}
	if !have.unsure.IsZero() {
		f.UnsureUntil = have.unsure.UnixNano()
	}
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("save the paste guard's memory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".paste-guard-*")
	if err != nil {
		return fmt.Errorf("save the paste guard's memory: %w", err)
	}
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save the paste guard's memory: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save the paste guard's memory: %w", err)
	}
	return nil
}
