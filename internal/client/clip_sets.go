package client

import (
	"slices"
	"sync/atomic"
	"time"
)

// The lists of the paste guard's memory (clip_memory.go). The clipboard
// has one list. Each reading has a list of its own for selections, such as
// the primary one: a selection holds only its last write, and the two
// readings may disagree on which write that is. A write that both readings
// see goes in both lists, and the writes of one reading never push out the
// other's.

type clipSums = map[[32]byte]time.Time

// clipWrites is the writes of one part of the output, in their order: the
// clipboard's, each reading's writes to a selection, and the time until
// which every drop is refused, or zero.
type clipWrites struct {
	board  [][32]byte
	sel    [clipReadings][][32]byte
	unsure time.Time
}

// add keeps writes that a reading saw, to the clipboard or, with sel, to a
// selection.
func (w *clipWrites) add(reading int, sel bool, sums ...[32]byte) {
	if sel {
		w.sel[reading] = append(w.sel[reading], sums...)
	} else {
		w.board = append(w.board, sums...)
	}
}

func (w *clipWrites) unsureUntil(t time.Time) {
	if t.After(w.unsure) {
		w.unsure = t
	}
}

// lists is the clipboard's writes, then each reading's selection writes.
func (w *clipWrites) lists() [][][32]byte {
	return append([][][32]byte{w.board}, w.sel[:]...)
}

func (w *clipWrites) empty() bool {
	for _, l := range w.lists() {
		if len(l) > 0 {
			return false
		}
	}
	return w.unsure.IsZero()
}

// size is the number of times that the writes take: the length of the
// longest list, or 1.
func (w *clipWrites) size() int {
	n := 1
	for _, l := range w.lists() {
		n = max(n, len(l))
	}
	return n
}

func (w *clipWrites) reset() {
	w.board = w.board[:0]
	for i := range w.sel {
		w.sel[i] = w.sel[i][:0]
	}
	w.unsure = time.Time{}
}

// clipSets is the clipboard's writes, each reading's selection writes, and
// the time until which every drop is refused, because a reader lost track
// of a write or the clock moved back.
type clipSets struct {
	board  clipSums
	sel    [clipReadings]clipSums
	unsure time.Time
}

func newClipSets() clipSets {
	s := clipSets{board: clipSums{}}
	for i := range s.sel {
		s.sel[i] = clipSums{}
	}
	return s
}

// lists is the clipboard's list, then each reading's selection list.
func (s *clipSets) lists() []clipSums { return append([]clipSums{s.board}, s.sel[:]...) }

func (s clipSets) clone() clipSets {
	c := clipSets{board: clonedSums(s.board), unsure: s.unsure}
	for i := range s.sel {
		c.sel[i] = clonedSums(s.sel[i])
	}
	return c
}

func clonedSums(m clipSums) clipSums {
	c := make(clipSums, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (s clipSets) empty() bool {
	for _, l := range s.lists() {
		if len(l) > 0 {
			return false
		}
	}
	return s.unsure.IsZero()
}

// has reports whether any list holds the write s.
func (s clipSets) has(sum [32]byte) bool {
	for _, l := range s.lists() {
		if _, ok := l[sum]; ok {
			return true
		}
	}
	return false
}

// addWrites adds writes in their order: the first of each list at at, and
// each next one 1 ns later. Each list keeps its newest clipKeep.
func (s *clipSets) addWrites(w *clipWrites, at time.Time) {
	for i, l := range w.lists() {
		addSums(s.lists()[i], l, at)
	}
	s.unsureUntil(w.unsure)
}

func addSums(into clipSums, sums [][32]byte, at time.Time) {
	if len(sums) == 0 {
		return
	}
	for i, s := range sums {
		t := at.Add(time.Duration(i))
		if old, ok := into[s]; !ok || t.After(old) {
			into[s] = t
		}
	}
	keepNewest(into)
}

// join adds the writes of o, at the later time of each.
func (s *clipSets) join(o clipSets) {
	for i, l := range o.lists() {
		joinSums(s.lists()[i], l)
	}
	s.unsureUntil(o.unsure)
}

func joinSums(dst, src clipSums) {
	for k, at := range src {
		if t, ok := dst[k]; !ok || at.After(t) {
			dst[k] = at
		}
	}
}

// forgetSaved drops the writes that the file now has, unless they came
// again since.
func (s *clipSets) forgetSaved(saved clipSets) {
	for i, l := range saved.lists() {
		pending := s.lists()[i]
		for sum, at := range l {
			if pending[sum].Equal(at) {
				delete(pending, sum)
			}
		}
	}
	if s.unsure.Equal(saved.unsure) {
		s.unsure = time.Time{}
	}
}

// unsureUntil refuses every drop until t, or later.
func (s *clipSets) unsureUntil(t time.Time) {
	if t.After(s.unsure) {
		s.unsure = t
	}
}

// prune drops writes older than clipMaxAge, and an unsure time that has
// passed. A write more than clipSkew ahead of the clock means that the
// clock moved back, so the order of the writes is not known: the write
// counts as now, and every drop is refused for a day. An unsure time more
// than a day ahead counts as a day from now.
func (s *clipSets) prune(now time.Time) {
	ahead := false
	for _, l := range s.lists() {
		ahead = pruneSums(l, now) || ahead
	}
	if ahead {
		s.unsureUntil(now.Add(clipMaxAge))
	}
	switch {
	case !s.unsure.After(now):
		s.unsure = time.Time{}
	case s.unsure.After(now.Add(clipMaxAge + clipSkew)):
		s.unsure = now.Add(clipMaxAge)
	}
}

// pruneSums drops writes older than clipMaxAge, and sets a time more than
// clipSkew ahead of the clock to now. ahead reports such a time.
func pruneSums(have clipSums, now time.Time) (ahead bool) {
	for s, at := range have {
		switch {
		case at.After(now.Add(clipSkew)):
			have[s], ahead = now, true
		case now.Sub(at) > clipMaxAge:
			delete(have, s)
		}
	}
	return ahead
}

// keepNewest keeps the newest clipKeep writes of each list.
func (s *clipSets) keepNewest() {
	for _, l := range s.lists() {
		keepNewest(l)
	}
}

// keepSorts counts the sorts of keepNewest, for the tests.
var keepSorts atomic.Int64

// keepNewest drops the oldest writes beyond clipKeep, in one sort. It
// keeps every write that shares the time of the last one kept.
func keepNewest(have clipSums) {
	if len(have) <= clipKeep {
		return
	}
	keepSorts.Add(1)
	times := make([]int64, 0, len(have))
	for _, at := range have {
		times = append(times, at.UnixNano())
	}
	slices.Sort(times)
	cut := times[len(times)-clipKeep]
	for s, at := range have {
		if at.UnixNano() < cut {
			delete(have, s)
		}
	}
}
