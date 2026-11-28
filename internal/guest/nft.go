package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"

	"github.com/iamlakshya/simstim/internal/egress"
)

const (
	// DefaultTransparentPort is the loopback port where the microvm guest
	// transparent TCP shim listens for nftables-redirected flows (spec 7.2).
	DefaultTransparentPort = 3129
	// DefaultTransparentAddr is the loopback address for TransparentProxy.
	DefaultTransparentAddr = "127.0.0.1:3129"
	// DockerTransparentAddr binds all guest IPv4 interfaces when --docker is
	// enabled so nftables prerouting redirect from docker0 (which rewrites the
	// destination to docker0's primary IPv4 address) reaches TransparentProxy.
	// A microvm has no external NIC.
	DockerTransparentAddr = "0.0.0.0:3129"
	// DummyIfaceName is the unrouted guest interface holding the default route
	// in a microvm so outbound TCP reaches the nftables output redirect hook.
	DummyIfaceName = "dummy0"
	// DummyPeerName is the peer interface used when the guest kernel lacks
	// CONFIG_DUMMY and dummy0 is created as a veth pair instead.
	DummyPeerName = "dummy0p"
	// DummyIfaceIP is the unrouted RFC 5737 stub IPv4 address assigned to dummy0.
	DummyIfaceIP = "192.0.2.1"
	// DummyIfacePrefixLen is the prefix length for DummyIfaceIP (/30).
	DummyIfacePrefixLen = 30
)

// TransparentProxy listens on a loopback TCP4 address inside a microvm guest
// (default 127.0.0.1:3129), recovers each redirected connection's original
// destination via SO_ORIGINAL_DST, and forwards the raw TCP stream over a
// KindEgress link stream with via="transparent" (isolation spec 7.2).
type TransparentProxy struct {
	opener     StreamOpener
	listener   net.Listener
	listenPort uint16
	origDst    func(net.Conn) (netip.AddrPort, error)
	wg         sync.WaitGroup
}

func transparentListenAddr(docker bool) string {
	if docker {
		return DockerTransparentAddr
	}
	return DefaultTransparentAddr
}

// ListenTransparent binds addr (default 127.0.0.1:3129) inside the sandbox's
// network namespace for nftables-redirected TCP connections.
func ListenTransparent(ctx context.Context, opener StreamOpener, addr string) (*TransparentProxy, error) {
	return listenTransparentWith(ctx, opener, addr, originalDst)
}

func listenTransparentWith(ctx context.Context, opener StreamOpener, addr string, origDst func(net.Conn) (netip.AddrPort, error)) (*TransparentProxy, error) {
	if opener == nil {
		return nil, errors.New("guest transparent proxy: opener is required. Try: pass an egress link opener")
	}
	if addr == "" {
		addr = DefaultTransparentAddr
	}
	if origDst == nil {
		origDst = originalDst
	}
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("guest transparent proxy: listen on %s: %w. Try: check loopback inside the microvm", addr, err)
	}
	var listenPort uint16
	if ta, ok := ln.Addr().(*net.TCPAddr); ok && ta.Port > 0 && ta.Port <= 65535 {
		listenPort = uint16(ta.Port)
	}
	return &TransparentProxy{
		opener:     opener,
		listener:   ln,
		listenPort: listenPort,
		origDst:    origDst,
	}, nil
}

// Addr returns the transparent proxy's listening address.
func (p *TransparentProxy) Addr() net.Addr {
	if p == nil || p.listener == nil {
		return nil
	}
	return p.listener.Addr()
}

// Serve accepts redirected TCP connections until ctx ends or Close is called.
func (p *TransparentProxy) Serve(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	defer stop()
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			break
		}
		p.wg.Add(1)
		go func(c net.Conn) {
			defer p.wg.Done()
			p.forward(ctx, c)
		}(conn)
	}
	p.wg.Wait()
}

func (p *TransparentProxy) forward(ctx context.Context, client net.Conn) {
	defer func() { _ = client.Close() }()
	dst, err := p.origDst(client)
	if err != nil || shouldRefuseTransparentDst(dst, client.LocalAddr(), p.listenPort) {
		return
	}
	st, err := p.opener.OpenEgress(ctx, egress.StreamHeader{
		Via:  "transparent",
		Host: dst.Addr().String(),
		Port: int(dst.Port()),
	})
	if err != nil {
		return
	}
	defer func() { _ = st.Close() }()
	stop := context.AfterFunc(ctx, func() {
		_ = client.Close()
		_ = st.Close()
	})
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(st, client)
		_ = st.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, st)
		if cw, ok := client.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = client.Close()
		}
	}()
	wg.Wait()
}

// Close closes the transparent proxy listener.
func (p *TransparentProxy) Close() error {
	if p == nil || p.listener == nil {
		return nil
	}
	if err := p.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("guest transparent proxy: close listener: %w", err)
	}
	return nil
}

// shouldRefuseTransparentDst guards TransparentProxy against self-loops when
// listening on 127.0.0.1:3129 or 0.0.0.0:3129 (with --docker enabled).
func shouldRefuseTransparentDst(dst netip.AddrPort, localAddr net.Addr, listenPort uint16) bool {
	if !dst.IsValid() || !dst.Addr().IsValid() || dst.Port() == 0 {
		return true
	}
	addr := dst.Addr().Unmap()
	if addr.IsLoopback() || addr.IsUnspecified() {
		return true
	}
	if ta, ok := localAddr.(*net.TCPAddr); ok {
		if lap := ta.AddrPort(); lap.IsValid() && lap.Addr().Unmap() == addr && lap.Port() == dst.Port() {
			return true
		}
	}
	if listenPort > 0 && dst.Port() == listenPort {
		if addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr == netip.MustParseAddr(DummyIfaceIP) {
			return true
		}
	}
	return false
}
