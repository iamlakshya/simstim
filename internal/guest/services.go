package guest

import (
	"context"
	"sync"
)

type loopbackServices struct {
	proxy       *Proxy
	dns         *DNSStub
	transparent *TransparentProxy
}

func startLoopbackServices(ctx context.Context, r *runner, opts RunOptions, wg *sync.WaitGroup) (*loopbackServices, error) {
	svc := &loopbackServices{}
	if opts.Proxy {
		px, err := ListenProxy(ctx, r, opts.ProxyAddrs...)
		if err != nil {
			return nil, err
		}
		svc.proxy = px
		wg.Add(1)
		go func() {
			defer wg.Done()
			px.Serve(ctx)
		}()
	}
	if opts.DNS || opts.DNSAddr != "" {
		dns, err := ListenDNS(ctx, r, opts.DNSAddr)
		if err != nil {
			svc.close()
			return nil, err
		}
		svc.dns = dns
		if opts.OnDNSReady != nil {
			opts.OnDNSReady(dns.Addr(), dns.TCPAddr())
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			dns.Serve(ctx)
		}()
	}
	if opts.Transparent || opts.TransparentAddr != "" {
		addr := opts.TransparentAddr
		if addr == "" {
			addr = transparentListenAddr(opts.Docker)
		}
		tp, err := ListenTransparent(ctx, r, addr)
		if err != nil {
			svc.close()
			return nil, err
		}
		svc.transparent = tp
		if opts.OnTransparentReady != nil {
			opts.OnTransparentReady(tp.Addr())
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			tp.Serve(ctx)
		}()
	}
	return svc, nil
}

func (s *loopbackServices) close() {
	if s == nil {
		return
	}
	if s.proxy != nil {
		_ = s.proxy.Close()
	}
	if s.dns != nil {
		_ = s.dns.Close()
	}
	if s.transparent != nil {
		_ = s.transparent.Close()
	}
}
