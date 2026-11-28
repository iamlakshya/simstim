package guest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
)

const (
	maxGuestDNSBytes = 4096
	dnsStreamTimeout = 10 * time.Second
)

// DNSOpener opens a KindDNS link stream to the trusted egress process.
type DNSOpener interface {
	OpenDNS(ctx context.Context) (*link.Stream, error)
}

// DNSStub listens on UDP and TCP loopback sockets inside the sandbox's own
// network namespace (spec 7.2: 127.0.0.53:53 when available) and forwards each
// raw DNS wire query over a KindDNS link stream to the egress process.
type DNSStub struct {
	opener DNSOpener
	pc     net.PacketConn
	ln     net.Listener
	wg     sync.WaitGroup
}

// ListenDNS binds matching UDP and TCP listeners on addr inside the sandbox's
// network namespace.
func ListenDNS(ctx context.Context, opener DNSOpener, addr string) (*DNSStub, error) {
	if opener == nil {
		return nil, errors.New("guest dns: opener is required. Try: pass a DNS link opener")
	}
	if addr == "" {
		addr = "127.0.0.53:53"
	}
	lc := net.ListenConfig{}
	pc, err := lc.ListenPacket(ctx, "udp", addr)
	if err != nil {
		return nil, fmt.Errorf("guest dns: listen UDP on %s: %w. Try: check sandbox loopback permissions", addr, err)
	}
	tcpAddr := pc.LocalAddr().String()
	ln, err := lc.Listen(ctx, "tcp", tcpAddr)
	if err != nil {
		// If the caller asked for an ephemeral port (:0) and another process
		// already bound the same TCP port, fall back to an ephemeral TCP port.
		if _, p, serr := net.SplitHostPort(addr); serr == nil && p == "0" {
			ln, err = lc.Listen(ctx, "tcp", addr)
		}
		if err != nil {
			_ = pc.Close()
			return nil, fmt.Errorf("guest dns: listen TCP on %s: %w. Try: check sandbox loopback permissions", tcpAddr, err)
		}
	}
	return &DNSStub{opener: opener, pc: pc, ln: ln}, nil
}

// NewDNSStub wraps an already-bound UDP PacketConn (for example a pre-bound
// socket descriptor passed into the sandbox).
func NewDNSStub(opener DNSOpener, pc net.PacketConn) (*DNSStub, error) {
	return NewDNSStubWithTCP(opener, pc, nil)
}

// NewDNSStubWithTCP wraps already-bound UDP and optional TCP sockets passed
// into the sandbox.
func NewDNSStubWithTCP(opener DNSOpener, pc net.PacketConn, ln net.Listener) (*DNSStub, error) {
	if opener == nil || pc == nil {
		return nil, errors.New("guest dns: opener and PacketConn are required. Try: pass both")
	}
	return &DNSStub{opener: opener, pc: pc, ln: ln}, nil
}

// Addr returns the stub's local UDP address.
func (d *DNSStub) Addr() net.Addr { return d.pc.LocalAddr() }

// TCPAddr returns the stub's local TCP address, or nil when TCP is not bound.
func (d *DNSStub) TCPAddr() net.Addr {
	if d.ln == nil {
		return nil
	}
	return d.ln.Addr()
}

// Serve reads UDP and TCP DNS queries and answers them via KindDNS link streams
// until ctx ends or Close is called.
func (d *DNSStub) Serve(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = d.Close() })
	defer stop()

	if d.ln != nil {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.serveTCP(ctx)
		}()
	}

	buf := make([]byte, maxGuestDNSBytes)
	for {
		n, peer, err := d.pc.ReadFrom(buf)
		if err != nil {
			break
		}
		if n == 0 || n > maxGuestDNSBytes {
			continue
		}
		query := make([]byte, n)
		copy(query, buf[:n])
		d.wg.Add(1)
		go func(q []byte, dst net.Addr) {
			defer d.wg.Done()
			if resp, ok := d.roundTrip(ctx, q); ok {
				_, _ = d.pc.WriteTo(resp, dst)
			}
		}(query, peer)
	}
	d.wg.Wait()
}

func (d *DNSStub) serveTCP(ctx context.Context) {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			return
		}
		d.wg.Add(1)
		go func(c net.Conn) {
			defer d.wg.Done()
			d.handleTCPConn(ctx, c)
		}(conn)
	}
}

func (d *DNSStub) handleTCPConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var lenBuf [2]byte
	for {
		_ = conn.SetDeadline(time.Now().Add(dnsStreamTimeout))
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		qLen := int(binary.BigEndian.Uint16(lenBuf[:]))
		if qLen == 0 || qLen > maxGuestDNSBytes {
			return
		}
		query := make([]byte, qLen)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		resp, ok := d.roundTrip(ctx, query)
		if !ok {
			return
		}
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(resp))) //nolint:gosec // G115: len(resp) <= maxGuestDNSBytes (4096)
		if _, err := conn.Write(lenBuf[:]); err != nil {
			return
		}
		if _, err := conn.Write(resp); err != nil {
			return
		}
	}
}

func (d *DNSStub) roundTrip(ctx context.Context, query []byte) ([]byte, bool) {
	qctx, cancel := context.WithTimeout(ctx, dnsStreamTimeout)
	defer cancel()

	st, err := d.opener.OpenDNS(qctx)
	if err != nil {
		return nil, false
	}
	defer func() { _ = st.Close() }()
	_ = st.SetDeadline(time.Now().Add(dnsStreamTimeout))

	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(query))) //nolint:gosec // G115: len(query) <= maxGuestDNSBytes (4096)
	if _, err := st.Write(lenBuf[:]); err != nil {
		return nil, false
	}
	if _, err := st.Write(query); err != nil {
		return nil, false
	}
	_ = st.CloseWrite()

	if _, err := io.ReadFull(st, lenBuf[:]); err != nil {
		return nil, false
	}
	respLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if respLen == 0 || respLen > maxGuestDNSBytes {
		return nil, false
	}
	resp := make([]byte, respLen)
	if _, err := io.ReadFull(st, resp); err != nil {
		return nil, false
	}
	return resp, true
}

// Close closes the guest DNS UDP and TCP sockets.
func (d *DNSStub) Close() error {
	var first error
	if err := d.pc.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		first = fmt.Errorf("guest dns: close UDP socket: %w", err)
	}
	if d.ln != nil {
		if err := d.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) && first == nil {
			first = fmt.Errorf("guest dns: close TCP listener: %w", err)
		}
	}
	return first
}
