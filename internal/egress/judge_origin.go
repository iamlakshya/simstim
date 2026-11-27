package egress

import (
	"net"
	"sync"
)

// judgeOrigin owns the one native connection of a fixed-endpoint model call.
// Its admission mutex fences dial and IO admission against physical closure.
type judgeOrigin struct {
	mu      sync.Mutex
	closing bool
	conn    *judgeOriginConn
	dials   sync.WaitGroup
	once    sync.Once
	done    chan struct{}
}

type judgeOriginConn struct {
	net.Conn
	mu     sync.Mutex
	closed bool
	io     sync.WaitGroup
	once   sync.Once
	err    error
}

func newJudgeOrigin() *judgeOrigin { return &judgeOrigin{done: make(chan struct{})} }

func (origin *judgeOrigin) beginDial() bool {
	origin.mu.Lock()
	defer origin.mu.Unlock()
	if origin.closing {
		return false
	}
	origin.dials.Add(1)
	return true
}

func (origin *judgeOrigin) attach(conn net.Conn) (net.Conn, error) {
	origin.mu.Lock()
	if origin.closing || origin.conn != nil {
		origin.mu.Unlock()
		_ = conn.Close()
		return nil, errJudgeTransport
	}
	owned := &judgeOriginConn{Conn: conn}
	origin.conn = owned
	origin.mu.Unlock()
	return owned, nil
}

func (origin *judgeOrigin) close() {
	origin.once.Do(func() {
		origin.mu.Lock()
		origin.closing = true
		conn := origin.conn
		origin.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		// Late dial completion is rejected and closed by attach before its
		// caller marks that admitted dial finished.
		origin.dials.Wait()
		close(origin.done)
	})
	<-origin.done
}

func (conn *judgeOriginConn) beginIO() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.closed {
		return false
	}
	conn.io.Add(1)
	return true
}

func (conn *judgeOriginConn) Read(buffer []byte) (int, error) {
	if !conn.beginIO() {
		return 0, net.ErrClosed
	}
	defer conn.io.Done()
	return conn.Conn.Read(buffer)
}

func (conn *judgeOriginConn) Write(buffer []byte) (int, error) {
	if !conn.beginIO() {
		return 0, net.ErrClosed
	}
	defer conn.io.Done()
	return conn.Conn.Write(buffer)
}

func (conn *judgeOriginConn) Close() error {
	conn.once.Do(func() {
		conn.mu.Lock()
		conn.closed = true
		conn.mu.Unlock()
		conn.err = conn.Conn.Close()
		conn.io.Wait()
	})
	return conn.err
}
