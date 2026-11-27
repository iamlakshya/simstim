package link

import (
	"bufio"
	"net"
)

// ContinueExec takes exclusive ownership after an ordinary, verified hello and
// successful exec response. reader MUST be the reader that read that response:
// it may already hold stream bytes. No second hello is sent or consumed.
// The host is Supervisor (odd stream IDs); the client is Guest.
func ContinueExec(conn net.Conn, reader *bufio.Reader, side Side) *Session {
	s := newSession(conn, side, Options{Window: DefaultWindow, MaxSupervisorStreams: 1, MaxGuestStreams: 1, Opener: map[Kind]Side{KindExec: Supervisor}})
	s.helloOK = true
	close(s.hello)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.fail(s.readContinuation(reader)) }()
	go func() { defer s.wg.Done(); s.writeContinuation() }()
	return s
}
