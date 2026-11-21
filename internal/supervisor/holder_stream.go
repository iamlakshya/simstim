package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proc"
	"github.com/iamlakshya/simstim/internal/proto"
)

// streamTimeout bounds the open of a holder stream and the holder's hello.
const streamTimeout = 10 * time.Second

// guest opens the connection's holder stream, when it has none. It passes
// the client's hello, reads the holder's, which the supervisor answered
// for already, then passes the holder's frames back to the client, and
// the client's to the holder (forward).
func (s *Supervisor) guest(c *client) error {
	c.gmu.Lock()
	defer c.gmu.Unlock()
	if c.gs != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	defer cancel()
	st, err := s.link.Open(ctx, link.KindHolder, map[string]string{"session": s.id})
	if err != nil {
		return fmt.Errorf("open a holder stream: %w", err)
	}
	gw := proto.NewWriter(st)
	gr := bufio.NewReaderSize(st, 64<<10)
	_ = st.SetDeadline(time.Now().Add(streamTimeout))
	if err := gw.Write(proto.THello, c.hello); err != nil {
		_ = st.Close()
		return fmt.Errorf("pass the hello: %w", err)
	}
	if err := skipHello(gr); err != nil {
		_ = st.Close()
		return err
	}
	_ = st.SetDeadline(time.Time{})
	// From now on, the client's reader may wait for the holder: the
	// client's end closes the connection, whatever the reader does.
	c.gs, c.watch = st, proc.WatchReadPeerGone(c.conn, c.closeSoon)
	c.streamWorkers.Add(2)
	go func() { defer c.streamWorkers.Done(); s.relay(c, st, gr) }()
	go func() { defer c.streamWorkers.Done(); s.forward(c, gw) }()
	return nil
}
