package guest

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
)

const portDialTimeout = 3 * time.Second

// servePort relays one link.KindPort stream to a listening TCP port on the
// guest's own loopback interface (127.0.0.1, falling back to ::1).
func (r *runner) servePort(ctx context.Context, st *link.Stream) {
	defer st.Close()
	var hdr PortHeader
	if err := st.Header(&hdr); err != nil || hdr.Port < 1 || hdr.Port > 65535 {
		return
	}
	conn, err := dialLoopbackPort(ctx, hdr.Port)
	if err != nil {
		return
	}
	defer conn.Close()

	// Tear down the TCP connection promptly if guest init shuts down.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyOneWay(conn, st)
	}()
	go func() {
		defer wg.Done()
		copyOneWay(st, conn)
	}()
	wg.Wait()
}

func dialLoopbackPort(ctx context.Context, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: portDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		return conn, nil
	}
	conn6, err6 := d.DialContext(ctx, "tcp", fmt.Sprintf("[::1]:%d", port))
	if err6 == nil {
		return conn6, nil
	}
	return nil, fmt.Errorf("dial guest port %d: %w", port, err)
}
