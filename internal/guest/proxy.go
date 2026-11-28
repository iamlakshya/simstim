// Package guest holds the small loopback shims that run inside a walled
// session's own isolated network namespace: the explicit HTTP proxy shim,
// the DNS stub, and the proxy environment builder. Neither shim listens on
// the host's network namespace (decisions 0030 and 0031).
package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
)

// DefaultProxyAddrs are the guest-loopback addresses that spec 7.2 binds inside
// the sandbox's own network namespace.
var DefaultProxyAddrs = []string{"127.0.0.1:3128", "[::1]:3128"}

// StreamOpener opens an egress link stream to the host's egress process.
type StreamOpener interface {
	OpenEgress(ctx context.Context, hdr egress.StreamHeader) (*link.Stream, error)
}

// Proxy listens on loopback TCP socket(s) inside the sandbox's network
// namespace and forwards every connection verbatim over a KindEgress link
// stream to the trusted egress process. It parses no HTTP inside the guest.
type Proxy struct {
	opener    StreamOpener
	listeners []net.Listener
	wg        sync.WaitGroup
}

// ListenProxy binds addrs inside the caller's network namespace. If addrs is
// empty, it binds 127.0.0.1:3128 and, when IPv6 loopback is available,
// [::1]:3128.
func ListenProxy(ctx context.Context, opener StreamOpener, addrs ...string) (*Proxy, error) {
	if opener == nil {
		return nil, errors.New("guest proxy: opener is required. Try: pass an egress link opener")
	}
	lc := net.ListenConfig{}
	if len(addrs) == 0 {
		l4, err := lc.Listen(ctx, "tcp4", "127.0.0.1:3128")
		if err != nil {
			return nil, fmt.Errorf("guest proxy: listen on 127.0.0.1:3128: %w. Try: check loopback inside the sandbox", err)
		}
		listeners := []net.Listener{l4}
		if l6, err := lc.Listen(ctx, "tcp6", "[::1]:3128"); err == nil {
			listeners = append(listeners, l6)
		}
		return &Proxy{opener: opener, listeners: listeners}, nil
	}
	listeners := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		l, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			for _, prev := range listeners {
				_ = prev.Close()
			}
			return nil, fmt.Errorf("guest proxy: listen on %s: %w. Try: check the guest loopback address", addr, err)
		}
		listeners = append(listeners, l)
	}
	return &Proxy{opener: opener, listeners: listeners}, nil
}

// Addr returns the primary listener's address.
func (p *Proxy) Addr() net.Addr {
	if len(p.listeners) == 0 {
		return nil
	}
	return p.listeners[0].Addr()
}

// Serve accepts guest connections until ctx is cancelled or Close is called.
func (p *Proxy) Serve(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	defer stop()
	for _, l := range p.listeners {
		p.wg.Add(1)
		go func(lis net.Listener) {
			defer p.wg.Done()
			p.acceptLoop(ctx, lis)
		}(l)
	}
	p.wg.Wait()
}

func (p *Proxy) acceptLoop(ctx context.Context, l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func(c net.Conn) {
			defer p.wg.Done()
			p.forward(ctx, c)
		}(conn)
	}
}

type closeWriter interface {
	CloseWrite() error
}

func (p *Proxy) forward(ctx context.Context, client net.Conn) {
	defer func() { _ = client.Close() }()
	st, err := p.opener.OpenEgress(ctx, egress.StreamHeader{Via: "explicit"})
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

// Close closes every guest loopback listener.
func (p *Proxy) Close() error {
	var first error
	for _, l := range p.listeners {
		if err := l.Close(); err != nil && first == nil && !errors.Is(err, net.ErrClosed) {
			first = err
		}
	}
	return first
}
