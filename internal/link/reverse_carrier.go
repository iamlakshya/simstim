package link

import (
	"bufio"
	"net"
)

// ContinueReverse takes the same buffered connection after its verified hello
// and successful reverse response. Only the host may open rport streams here;
// the normal supervisor/guest link keeps its original opener table.
func ContinueReverse(conn net.Conn, reader *bufio.Reader, side Side) *Session {
	s := newSession(conn, side, Options{Window: DefaultWindow, MaxSupervisorStreams: 32, MaxGuestStreams: 1,
		Opener: map[Kind]Side{KindRPort: Supervisor}})
	s.helloOK = true
	close(s.hello)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.fail(s.readContinuation(reader)) }()
	go func() { defer s.wg.Done(); s.writeContinuation() }()
	return s
}
