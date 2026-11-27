package supervisor

import (
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
)

// The reader belongs to the actual one guest link. Its done channel proves
// only local reader/worker completion, never that a VM or native role exited.
type guestStreamReaderV1 struct {
	link    *link.Session
	control *ctlConn
	done    chan struct{}
	close   sync.Once
}

func (s *Supervisor) startGuestStreamReaderV1() error {
	s.mu.Lock()
	if s.guestStreams != nil || s.link == nil || s.ctl == nil || !s.unwalledV1() && (s.eg == nil || s.eg.sess == nil || s.eg.ctl == nil || s.eg.dead.Load()) {
		s.mu.Unlock()
		return execRefusal("the original guest stream reader has no ready owned link and egress control")
	}
	reader := &guestStreamReaderV1{link: s.link, control: s.ctl, done: make(chan struct{})}
	s.guestStreams = reader
	s.mu.Unlock()
	go func() { defer close(reader.done); s.acceptGuestStreams(reader.link) }()
	return nil
}

func (s *Supervisor) closeGuestStreamReaderV1() {
	s.mu.Lock()
	reader, session, control := s.guestStreams, s.link, s.ctl
	s.mu.Unlock()
	if reader != nil {
		session, control = reader.link, reader.control
	}
	closeLink := func() {
		// Preserve the existing final-control grace before closing the link.
		// Runtime cleanup already joined the guest. This is local draining.
		if control != nil && control.done != nil {
			select {
			case <-control.done:
			case <-time.After(250 * time.Millisecond):
			}
		}
		if session != nil {
			_ = session.Close()
		}
	}
	if reader == nil {
		closeLink()
		return
	}
	reader.close.Do(closeLink)
	<-reader.done
}
