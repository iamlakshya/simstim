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

// DNSStub listens on a UDP socket inside the sandbox's own network namespace
// (spec 7.2: 127.0.0.53:53 when available) and forwards each raw DNS wire
// query over a KindDNS link stream to the egress process.
type DNSStub struct {
	opener DNSOpener
	pc     net.PacketConn
	wg     sync.WaitGroup
}

// ListenDNS binds a UDP listener on addr inside the sandbox's network
// namespace.
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
	return &DNSStub{opener: opener, pc: pc}, nil
}

// NewDNSStub wraps an already-bound PacketConn (for example a pre-bound socket
// descriptor passed into the sandbox).
func NewDNSStub(opener DNSOpener, pc net.PacketConn) (*DNSStub, error) {
	if opener == nil || pc == nil {
		return nil, errors.New("guest dns: opener and PacketConn are required. Try: pass both")
	}
	return &DNSStub{opener: opener, pc: pc}, nil
}

// Addr returns the stub's local UDP address.
func (d *DNSStub) Addr() net.Addr { return d.pc.LocalAddr() }

// Serve reads UDP DNS queries and answers them via KindDNS link streams until
// ctx ends or Close is called.
func (d *DNSStub) Serve(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = d.Close() })
	defer stop()
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
			d.handleQuery(ctx, q, dst)
		}(query, peer)
	}
	d.wg.Wait()
}

func (d *DNSStub) handleQuery(ctx context.Context, query []byte, peer net.Addr) {
	qctx, cancel := context.WithTimeout(ctx, dnsStreamTimeout)
	defer cancel()

	st, err := d.opener.OpenDNS(qctx)
	if err != nil {
		return
	}
	defer func() { _ = st.Close() }()
	_ = st.SetDeadline(time.Now().Add(dnsStreamTimeout))

	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(query))) //nolint:gosec // G115: len(query) <= maxGuestDNSBytes (4096)
	if _, err := st.Write(lenBuf[:]); err != nil {
		return
	}
	if _, err := st.Write(query); err != nil {
		return
	}
	_ = st.CloseWrite()

	if _, err := io.ReadFull(st, lenBuf[:]); err != nil {
		return
	}
	respLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if respLen == 0 || respLen > maxGuestDNSBytes {
		return
	}
	resp := make([]byte, respLen)
	if _, err := io.ReadFull(st, resp); err != nil {
		return
	}
	_, _ = d.pc.WriteTo(resp, peer)
}

// Close closes the guest DNS UDP socket.
func (d *DNSStub) Close() error {
	if err := d.pc.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("guest dns: close socket: %w", err)
	}
	return nil
}
