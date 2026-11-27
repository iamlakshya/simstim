package guest

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	// dnsMaxConcurrentQueries caps open KindDNS streams from the guest stub so
	// proxyMaxConcurrent (64) + dnsMaxConcurrentQueries (24) stays below
	// link.DefaultMaxGuestStreams (96).
	dnsMaxConcurrentQueries = 24
	dnsMaxTCPConns          = 16
	dnsMaxQueriesPerTCPConn = 16
	dnsIOTimeout            = 5 * time.Second

	dnsMinWireBytes     = 12
	dnsMaxWireBytes     = 65535
	dnsMinUDPPayload    = 512
	dnsMaxEDNSPayload   = 1232
	dnsUDPReadBufferLen = 65536
)

// DNS is the capability-free guest DNS stub (spec 4.1 and 7.2). It serves
// caller-supplied or launcher-inherited loopback UDP and TCP sockets and
// forwards each raw query over a separate KindDNS stream on the link.
type DNS struct {
	observe func([]byte, []byte) error
	sess    *link.Session
	udp     net.PacketConn
	tcp     net.Listener
	log     *slog.Logger
	sem     chan struct{}
	tcpSem  chan struct{}
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	streams map[*link.Stream]struct{}
}

// StartDNS verifies that socks are bound on loopback and starts serving UDP
// and TCP DNS queries over sess. On error, socks are closed.
func StartDNS(sess *link.Session, socks DNSSockets, log *slog.Logger) (*DNS, error) {
	return startDNSObserved(sess, socks, log, nil)
}

type dnsBindScope uint8

const (
	dnsLoopbackScope dnsBindScope = iota
	dnsPrivateVMScope
)

func startDNSObserved(sess *link.Session, socks DNSSockets, log *slog.Logger, observe func([]byte, []byte) error) (*DNS, error) {
	return startDNSScoped(sess, socks, log, observe, dnsLoopbackScope)
}

func startDNSScoped(sess *link.Session, socks DNSSockets, log *slog.Logger, observe func([]byte, []byte) error, scope dnsBindScope) (*DNS, error) {
	if sess == nil || socks.UDP == nil || socks.TCP == nil {
		_ = socks.Close()
		return nil, &proto.Error{
			Code:    "wall_init",
			Message: "the guest DNS stub requires a link session and both UDP and TCP sockets",
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}
	if err := verifyScopedDNSAddr(socks.UDP.LocalAddr(), scope); err != nil {
		_ = socks.Close()
		return nil, err
	}
	if err := verifyScopedDNSAddr(socks.TCP.Addr(), scope); err != nil {
		_ = socks.Close()
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	d := &DNS{observe: observe,
		sess:    sess,
		udp:     socks.UDP,
		tcp:     socks.TCP,
		log:     log,
		sem:     make(chan struct{}, dnsMaxConcurrentQueries),
		tcpSem:  make(chan struct{}, dnsMaxTCPConns),
		done:    make(chan struct{}),
		conns:   make(map[net.Conn]struct{}),
		streams: make(map[*link.Stream]struct{}),
	}
	d.wg.Add(2)
	go d.udpLoop()
	go d.tcpAcceptLoop()
	return d, nil
}

// UDPAddr returns the bound local address of the UDP DNS socket.
func (d *DNS) UDPAddr() net.Addr { return d.udp.LocalAddr() }

// TCPAddr returns the bound local address of the TCP DNS listener.
func (d *DNS) TCPAddr() net.Addr { return d.tcp.Addr() }

// Close stops both listeners, closes every active TCP client connection and
// in-flight KindDNS stream, and waits for stub goroutines to finish.
func (d *DNS) Close() error {
	d.once.Do(func() {
		close(d.done)
		_ = d.udp.Close()
		_ = d.tcp.Close()
		d.mu.Lock()
		for c := range d.conns {
			_ = c.Close()
		}
		for st := range d.streams {
			_ = st.SetDeadline(time.Now())
			_ = st.Close()
		}
		d.mu.Unlock()
	})
	d.wg.Wait()
	return nil
}

func (d *DNS) trackConn(c net.Conn) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.done:
		_ = c.Close()
		return false
	default:
		d.conns[c] = struct{}{}
		return true
	}
}

func (d *DNS) untrackConn(c net.Conn) {
	d.mu.Lock()
	delete(d.conns, c)
	d.mu.Unlock()
}

func (d *DNS) trackStream(st *link.Stream) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.done:
		_ = st.Close()
		return false
	default:
		d.streams[st] = struct{}{}
		return true
	}
}

func (d *DNS) untrackStream(st *link.Stream) {
	d.mu.Lock()
	delete(d.streams, st)
	d.mu.Unlock()
}

func (d *DNS) udpLoop() {
	defer d.wg.Done()
	buf := make([]byte, dnsUDPReadBufferLen)
	for {
		n, raddr, err := d.udp.ReadFrom(buf)
		if err != nil {
			select {
			case <-d.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if n < dnsMinWireBytes || n > dnsMaxWireBytes {
			continue
		}
		if buf[2]&0x80 != 0 {
			continue
		}
		query := make([]byte, n)
		copy(query, buf[:n])
		select {
		case d.sem <- struct{}{}:
			d.wg.Add(1)
			go func(q []byte, addr net.Addr) {
				defer d.wg.Done()
				defer func() { <-d.sem }()
				d.serveUDPQuery(q, addr)
			}(query, raddr)
		default:
		}
	}
}

func (d *DNS) serveUDPQuery(query []byte, addr net.Addr) {
	maxPayload := udpResponseLimit(query)
	resp, err := d.queryLink(query)
	if err != nil || len(resp) < dnsMinWireBytes {
		return
	}
	if len(resp) > maxPayload {
		resp = truncateUDPReply(resp, maxPayload)
	}
	_ = d.udp.SetWriteDeadline(time.Now().Add(dnsIOTimeout))
	_, _ = d.udp.WriteTo(resp, addr)
}

func (d *DNS) tcpAcceptLoop() {
	defer d.wg.Done()
	for {
		conn, err := d.tcp.Accept()
		if err != nil {
			select {
			case <-d.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		select {
		case d.tcpSem <- struct{}{}:
			if !d.trackConn(conn) {
				<-d.tcpSem
				continue
			}
			d.wg.Add(1)
			go func(c net.Conn) {
				defer d.wg.Done()
				defer func() { <-d.tcpSem }()
				d.serveTCPConn(c)
			}(conn)
		default:
			_ = conn.Close()
		}
	}
}

func (d *DNS) serveTCPConn(conn net.Conn) {
	defer d.untrackConn(conn)
	defer conn.Close()

	for range dnsMaxQueriesPerTCPConn {
		_ = conn.SetReadDeadline(time.Now().Add(dnsIOTimeout))
		var lenBuf [2]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		msgLen := int(binary.BigEndian.Uint16(lenBuf[:]))
		if msgLen < dnsMinWireBytes {
			return
		}
		query := make([]byte, msgLen)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		if query[2]&0x80 != 0 {
			return
		}

		if !d.acquireQuerySlot() {
			return
		}
		resp, err := d.queryLink(query)
		<-d.sem
		if err != nil || len(resp) < dnsMinWireBytes || len(resp) > dnsMaxWireBytes {
			return
		}

		_ = conn.SetWriteDeadline(time.Now().Add(dnsIOTimeout))
		var outLen [2]byte
		binary.BigEndian.PutUint16(outLen[:], uint16(len(resp))) //nolint:gosec // G115: checked len(resp) <= 65535
		bufs := net.Buffers{outLen[:], resp}
		if _, err := bufs.WriteTo(conn); err != nil {
			return
		}
	}
}

func (d *DNS) acquireQuerySlot() bool {
	timer := time.NewTimer(dnsIOTimeout)
	defer timer.Stop()
	select {
	case d.sem <- struct{}{}:
		return true
	case <-d.done:
		return false
	case <-timer.C:
		return false
	}
}

func (d *DNS) queryLink(query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsIOTimeout)
	defer cancel()

	stopDone := make(chan struct{})
	defer close(stopDone)
	go func() {
		select {
		case <-d.done:
			cancel()
		case <-stopDone:
		}
	}()

	st, err := d.sess.Open(ctx, link.KindDNS, nil)
	if err != nil {
		return nil, err
	}
	if !d.trackStream(st) {
		return nil, net.ErrClosed
	}
	defer d.untrackStream(st)
	defer st.Close()

	stopStream := context.AfterFunc(ctx, func() { _ = st.Close() })
	defer stopStream()
	_ = st.SetDeadline(time.Now().Add(dnsIOTimeout))

	if _, err := st.Write(query); err != nil {
		return nil, err
	}
	if err := st.CloseWrite(); err != nil {
		return nil, err
	}
	resp, err := io.ReadAll(io.LimitReader(st, dnsMaxWireBytes+1))
	if err != nil {
		return nil, err
	}
	if len(resp) > dnsMaxWireBytes {
		return nil, errors.New("DNS response exceeds maximum wire size")
	}
	if d.observe != nil {
		if err := d.observe(query, resp); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func udpResponseLimit(query []byte) int {
	var p dnsmessage.Parser
	hdr, err := p.Start(query)
	if err != nil || hdr.Response {
		return dnsMinUDPPayload
	}
	if _, err := p.Question(); err != nil {
		return dnsMinUDPPayload
	}
	if _, err := p.Question(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return dnsMinUDPPayload
	}
	if _, err := p.AnswerHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return dnsMinUDPPayload
	}
	if _, err := p.AuthorityHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return dnsMinUDPPayload
	}
	ahdr, err := p.AdditionalHeader()
	if err != nil || ahdr.Type != dnsmessage.TypeOPT || ahdr.Name.String() != "." {
		return dnsMinUDPPayload
	}
	size := int(ahdr.Class)
	if size < dnsMinUDPPayload {
		return dnsMinUDPPayload
	}
	if size > dnsMaxEDNSPayload {
		return dnsMaxEDNSPayload
	}
	return size
}

func truncateUDPReply(resp []byte, maxPayload int) []byte {
	var p dnsmessage.Parser
	hdr, err := p.Start(resp)
	if err != nil {
		return truncateHeaderOnly(resp)
	}
	q, qErr := p.Question()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 hdr.ID,
		Response:           true,
		OpCode:             hdr.OpCode,
		Truncated:          true,
		RecursionDesired:   hdr.RecursionDesired,
		RecursionAvailable: hdr.RecursionAvailable,
		RCode:              hdr.RCode,
	})
	if qErr == nil {
		if b.StartQuestions() == nil {
			_ = b.Question(q)
		}
	}
	pkt, err := b.Finish()
	if err != nil || len(pkt) > maxPayload {
		return truncateHeaderOnly(resp)
	}
	return pkt
}

func truncateHeaderOnly(resp []byte) []byte {
	var out [dnsMinWireBytes]byte
	copy(out[:4], resp[:4])
	out[2] = (out[2] & 0x79) | 0x82 // QR=1, AA=0, TC=1, preserve OpCode and RD
	out[3] = (out[3] & 0x8f)        // clear Z, AD and CD; preserve RA and RCode
	return out[:]
}
