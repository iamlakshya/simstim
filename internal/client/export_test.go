package client

import "time"

// SetLeasePeriod lets tests renew leases faster. It returns a function that
// puts the old period back.
func SetLeasePeriod(d time.Duration) func() {
	old := leasePeriod
	leasePeriod = d
	return func() { leasePeriod = old }
}

// SetEscFlush changes how long the input filter may hold an unfinished key
// sequence. It returns a function that puts the old time back.
func SetEscFlush(d time.Duration) func() {
	old := escFlush
	escFlush = d
	return func() { escFlush = old }
}

// AnswersPings reports whether RoundTrip times c's host with pings.
func AnswersPings(c *Conn) bool { return !c.noPong.Load() }

// ForgetClipMemory drops what this process holds of the paste guard's
// memory, as a new client process would start. The file stays.
func ForgetClipMemory() {
	clipMemMu.Lock()
	defer clipMemMu.Unlock()
	clear(clipMems)
}

// SetDropRead runs f after the guard opened the files of a drop and before
// the client reads them. It returns a function that takes it away.
func SetDropRead(f func()) func() {
	dropRead = f
	return func() { dropRead = nil }
}

// FlushClipMemory waits until this process's clipboard writes are in the
// paste guard's file, and its journal is folded.
func FlushClipMemory() {
	clipMemMu.Lock()
	mems := make([]*clipMemory, 0, len(clipMems))
	for _, m := range clipMems {
		mems = append(mems, m)
	}
	clipMemMu.Unlock()
	for _, m := range mems {
		m.flush(10 * time.Second)
	}
}
