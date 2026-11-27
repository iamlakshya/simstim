//go:build linux

package guest

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
)

type vmNetwork struct {
	proxy     *Proxy
	dns       *DNS
	synthetic *vmSyntheticMap
	listeners []net.Listener
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	active    map[io.Closer]struct{}
	wg        sync.WaitGroup
	once      sync.Once
}

func (v *vmRuntime) startNetwork() (*vmNetwork, error) {
	if err := setupVMRoutes(); err != nil {
		return nil, fmt.Errorf("prepare guest-only dummy network: %w", err)
	}
	ctx, cancel := context.WithCancel(v.ctx)
	n := &vmNetwork{ctx: ctx, cancel: cancel, synthetic: newVMSyntheticMap(), active: map[io.Closer]struct{}{}}
	failed := true
	defer func() {
		if failed {
			n.Close()
		}
	}()
	docker := v.plan.Args.VM.Docker
	proxyURL, dnsAddress, transparent4, transparent6 := "http://127.0.0.1:3128", "127.0.0.53:53", "127.0.0.1:3129", "[::1]:3129"
	scope := dnsLoopbackScope
	if docker {
		proxyURL, dnsAddress, transparent4, transparent6 = "http://0.0.0.0:3128", "0.0.0.0:53", "0.0.0.0:3129", "[::]:3129"
		scope = dnsPrivateVMScope
	}
	proxy, err := StartProxy(v.init.sess, proxyURL, v.init.log)
	if err != nil {
		return nil, err
	}
	n.proxy = proxy
	udp, err := net.ListenPacket("udp4", dnsAddress)
	if err != nil {
		return nil, err
	}
	tcp, err := net.Listen("tcp4", dnsAddress)
	if err != nil {
		udp.Close()
		return nil, err
	}
	dns, err := startDNSScoped(v.init.sess, DNSSockets{UDP: udp, TCP: tcp}, v.init.log, n.synthetic.observe, scope)
	if err != nil {
		return nil, err
	}
	n.dns = dns
	listener, err := net.Listen("tcp4", transparent4)
	if err != nil {
		return nil, err
	}
	n.listeners = append(n.listeners, listener)
	listener6, err := net.Listen("tcp6", transparent6)
	if err != nil {
		return nil, err
	}
	n.listeners = append(n.listeners, listener6)
	// Install only after all receivers exist. An error never starts an agent
	// behind a partially configured redirect or a second policy/resolver.
	if err := installVMRedirect(docker); err != nil {
		return nil, fmt.Errorf("redirect guest TCP: %w", err)
	}
	for _, listener := range n.listeners {
		n.wg.Add(1)
		go n.accept(v.init.sess, listener)
	}
	failed = false
	return n, nil
}

func (n *vmNetwork) Close() error {
	n.once.Do(func() {
		n.cancel()
		for _, listener := range n.listeners {
			listener.Close()
		}
		n.mu.Lock()
		for active := range n.active {
			if deadline, ok := active.(interface{ SetDeadline(time.Time) error }); ok {
				_ = deadline.SetDeadline(time.Now())
			}
			active.Close()
		}
		n.mu.Unlock()
		if n.dns != nil {
			n.dns.Close()
		}
		if n.proxy != nil {
			n.proxy.Close()
		}
	})
	n.wg.Wait()
	return nil
}

func (n *vmNetwork) track(closer io.Closer) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ctx.Err() != nil {
		return false
	}
	n.active[closer] = struct{}{}
	return true
}
func (n *vmNetwork) untrack(closer io.Closer) { n.mu.Lock(); delete(n.active, closer); n.mu.Unlock() }

func (n *vmNetwork) accept(session *link.Session, listener net.Listener) {
	defer n.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case n.proxy.sem <- struct{}{}:
			if !n.track(conn) {
				conn.Close()
				<-n.proxy.sem
				continue
			}
			n.wg.Add(1)
			go func() {
				defer n.wg.Done()
				defer func() { <-n.proxy.sem }()
				defer n.untrack(conn)
				defer conn.Close()
				n.forward(session, conn)
			}()
		default:
			conn.Close()
		}
	}
}

func (n *vmNetwork) forward(session *link.Session, conn net.Conn) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	target, err := vmOriginalDestination(tcp)
	if err != nil {
		return
	}
	host, err := n.synthetic.host(target.Addr())
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
	stream, err := session.Open(ctx, link.KindEgress, map[string]any{"via": "transparent", "host": host, "port": int(target.Port())})
	cancel()
	if err != nil {
		return
	}
	if !n.track(stream) {
		stream.Close()
		return
	}
	defer n.untrack(stream)
	defer stream.Close()
	// Raw bytes stay raw. The canonical trusted egress endpoint, not this
	// guest's synthetic hint/address, decides policy/SNI/addresses and TLS.
	finished := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(stream, conn); _ = stream.CloseWrite(); finished <- struct{}{} }()
	_, _ = io.Copy(conn, stream)
	conn.Close()
	stream.Close()
	<-finished
}
