package client

import "time"

// SetLeasePeriod lets tests renew leases faster. It returns a function that
// puts the old period back.
func SetLeasePeriod(d time.Duration) func() {
	old := leasePeriod
	leasePeriod = d
	return func() { leasePeriod = old }
}

// SetHelloWait changes how long a dial that allows no prompts waits for the
// bridge's hello. It returns a function that puts the old wait back.
func SetHelloWait(d time.Duration) func() {
	old := helloWait
	helloWait = d
	return func() { helloWait = old }
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
