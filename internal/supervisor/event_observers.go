package supervisor

import (
	"bufio"
	"errors"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"net"
	"time"
)

var errEventObserverAccepted = errors.New("native startup event observer accepted")

// startEventObserver hands only a read-only event request to the existing
// dispatcher. It never consumes the outside startup loan or owns boot intent.
// The original ancillary-aware line remains its sole physical reader.
func (s *Supervisor) startEventObserver(conn net.Conn, line *host.StartupLineV1, request proto.Request) error {
	s.observerMu.Lock()
	if s.observersClosed || len(s.observerLines) >= 8 {
		s.observerMu.Unlock()
		return execRefusal("the event observer owner is closed or has eight active startup observers")
	}
	if s.observerLines == nil {
		s.observerLines = make(map[*host.StartupLineV1]struct{})
	}
	s.observerLines[line] = struct{}{}
	s.observerWG.Add(1)
	s.observerMu.Unlock()
	_ = line.SetDeadline(time.Time{})
	go func() {
		defer s.observerWG.Done()
		defer func() { _ = line.Close(); s.observerMu.Lock(); delete(s.observerLines, line); s.observerMu.Unlock() }()
		client := &client{conn: conn, w: proto.NewWriter(conn), full: true, done: make(chan struct{}), writeWait: clientWriteWait}
		defer client.close()
		_ = s.serveEvents(client, bufio.NewReaderSize(line.OrdinaryReader(), 64<<10), request)
	}()
	return nil
}

// closeEventObservers revokes and joins all existing early connection owners
// before the native holder lease can be released or cancellation acknowledged.
func (s *Supervisor) closeEventObservers() error {
	s.observerMu.Lock()
	s.observersClosed = true
	lines := make([]*host.StartupLineV1, 0, len(s.observerLines))
	for line := range s.observerLines {
		lines = append(lines, line)
	}
	s.observerMu.Unlock()
	var result error
	for _, line := range lines {
		result = errors.Join(result, line.Close())
	}
	s.observerWG.Wait()
	return result
}
