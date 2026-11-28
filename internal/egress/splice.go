package egress

import (
	"context"
	"io"
	"net"
	"sync"
)

type closeWriter interface {
	CloseWrite() error
}

// spliceConnsWithBudget copies bytes bidirectionally while enforcing host's
// rolling upload budget when budget is non-nil and has a positive limit.
// It propagates half-closes when the connections support CloseWrite so
// protocols that rely on EOF work cleanly.
func spliceConnsWithBudget(ctx context.Context, client, upstream net.Conn, host string, budget *UploadBudget) (up, down int64, budgetHit bool) {
	stop := context.AfterFunc(ctx, func() {
		_ = client.Close()
		_ = upstream.Close()
	})
	defer stop()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if budget != nil && budget.Limit() > 0 && host != "" {
			up, budgetHit = copyUpWithBudget(upstream, client, host, budget)
			if budgetHit {
				_ = client.Close()
				_ = upstream.Close()
				return
			}
		} else {
			up, _ = io.Copy(upstream, client)
		}
		if cw, ok := upstream.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = upstream.Close()
		}
	}()
	go func() {
		defer wg.Done()
		down, _ = io.Copy(client, upstream)
		if cw, ok := client.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = client.Close()
		}
	}()
	wg.Wait()
	_ = client.Close()
	_ = upstream.Close()
	return up, down, budgetHit
}

// streamListener adapts accepted link streams (or any net.Conn) into a
// net.Listener so net/http.Server and goproxy run without opening any network
// port on the host (decision 0030).
type streamListener struct {
	addr   net.Addr
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

type listenerAddr string

func (a listenerAddr) Network() string { return "link" }
func (a listenerAddr) String() string  { return string(a) }

func newStreamListener(name string) *streamListener {
	return &streamListener{
		addr:   listenerAddr(name),
		conns:  make(chan net.Conn, 64),
		closed: make(chan struct{}),
	}
}

// Enqueue hands a new connection to the http.Server running on this listener.
func (l *streamListener) Enqueue(c net.Conn) error {
	select {
	case <-l.closed:
		return net.ErrClosed
	case l.conns <- c:
		return nil
	}
}

// Accept returns the next enqueued connection.
func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close stops accepting new connections and drains any queued ones.
func (l *streamListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		for {
			select {
			case c := <-l.conns:
				_ = c.Close()
			default:
				return
			}
		}
	})
	return nil
}

// Addr reports a synthetic link address.
func (l *streamListener) Addr() net.Addr { return l.addr }
