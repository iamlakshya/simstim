package link

import (
	"bufio"
	"net"
)

// ContinueInputs owns the same buffered connection after its authenticated
// input request was accepted. It sends no second hello. The client is Guest
// and may open only one put and one bundle; the phase owner checks exact scope,
// ordering and duplicates before accepting either stream.
func ContinueInputs(conn net.Conn, reader *bufio.Reader, side Side, bundle bool) *Session {
	s := newSession(conn, side, Options{Window: DefaultWindow, MaxSupervisorStreams: 1, MaxGuestStreams: 2,
		Opener: map[Kind]Side{KindPut: Guest, KindBundle: Guest}})
	s.inputContinuation = true
	s.inputBundleAllowed = bundle
	s.helloOK = true
	close(s.hello)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.fail(s.readContinuation(reader)) }()
	go func() { defer s.wg.Done(); s.writeContinuation() }()
	return s
}
