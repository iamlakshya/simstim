package link

import (
	"bufio"
	"net"
)

// ContinueEditorSSH takes the SAME accepted dedicated connection after its
// source-checked ssh.open response. All children stay on this one logical
// owner; no token can turn an independent connection into another child.
// The host is Supervisor and the client is Guest. Only the client opens work.
func ContinueEditorSSH(conn net.Conn, reader *bufio.Reader, side Side) *Session {
	s := newSession(conn, side, Options{Window: DefaultWindow, MaxHeader: 63 << 10,
		MaxSupervisorStreams: DefaultMaxGuestStreams, MaxGuestStreams: DefaultMaxGuestStreams,
		Opener: map[Kind]Side{KindCtl: Guest, KindExec: Guest, KindHolder: Guest, KindSFTP: Guest, KindPort: Guest}})
	s.helloOK = true
	close(s.hello)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.fail(s.readContinuation(reader)) }()
	go func() { defer s.wg.Done(); s.writeContinuation() }()
	return s
}
