package egress

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	dnsTypeA         uint16 = 1
	dnsTypeAAAA      uint16 = 28
	dnsClassINET     uint16 = 1
	dnsFlagsResponse uint16 = 0x8180
	dnsRcodeNXDomain uint16 = 3
)

type resolverCall struct {
	network string
	host    string
}

type recordingResolver struct {
	mu    sync.Mutex
	calls []resolverCall
	addrs []netip.Addr
	err   error
}

func (r *recordingResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	r.calls = append(r.calls, resolverCall{network: network, host: host})
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return slices.Clone(r.addrs), nil
}

func (r *recordingResolver) recorded() []resolverCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func TestDefaultResolverFallbackUsesRootedFQDN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pub := netip.MustParseAddr("203.0.113.10")
	stub := &recordingResolver{addrs: []netip.Addr{pub}}
	d := &Dialer{resolver: stub}

	addrs, rule, err := d.ResolveAndCheck(ctx, "allowed.example.com", 443, nil)
	if err != nil || rule != "" {
		t.Fatalf("ResolveAndCheck = (%v, %q, %v), want (%v, \"\", nil)", addrs, rule, err, []netip.Addr{pub})
	}
	if !slices.Equal(addrs, []netip.Addr{pub}) {
		t.Fatalf("ResolveAndCheck addrs = %v, want %v", addrs, []netip.Addr{pub})
	}
	wantCalls := []resolverCall{{network: "ip", host: "allowed.example.com."}}
	if got := stub.recorded(); !slices.Equal(got, wantCalls) {
		t.Fatalf("resolver calls = %v, want %v", got, wantCalls)
	}

	// Dial with nil Lookup also resolves the rooted FQDN and dials the verified IP.
	dialStub := &recordingResolver{addrs: []netip.Addr{pub}}
	var dialled []netip.AddrPort
	dDial := &Dialer{
		DialTCP: func(_ context.Context, ap netip.AddrPort) (net.Conn, error) {
			dialled = append(dialled, ap)
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return c1, nil
		},
		resolver: dialStub,
	}
	conn, ip, rule, err := dDial.Dial(ctx, "allowed.example.com", 443, nil)
	if err != nil || rule != "" || ip != pub {
		t.Fatalf("Dial = (%v, %q, %v), want (%s, \"\", nil)", ip, rule, err, pub)
	}
	_ = conn.Close()
	if got := dialStub.recorded(); !slices.Equal(got, wantCalls) {
		t.Fatalf("Dial resolver calls = %v, want %v", got, wantCalls)
	}
	if wantDial := []netip.AddrPort{netip.AddrPortFrom(pub, 443)}; !slices.Equal(dialled, wantDial) {
		t.Fatalf("dialled = %v, want %v", dialled, wantDial)
	}

	// Checked-address refusal and canonical host error formatting stay intact.
	meta := netip.MustParseAddr("169.254.169.254")
	refusedStub := &recordingResolver{addrs: []netip.Addr{pub, meta}}
	dRefused := &Dialer{resolver: refusedStub}
	_, rule, err = dRefused.ResolveAndCheck(ctx, "allowed.example.com", 443, nil)
	if !errors.Is(err, ErrRefusedAddr) || rule != "range:169.254.0.0/16" {
		t.Fatalf("refused ResolveAndCheck = (%q, %v), want range:169.254.0.0/16 and ErrRefusedAddr", rule, err)
	}
	if !strings.Contains(err.Error(), "allowed.example.com resolved to 169.254.169.254") {
		t.Errorf("refused error = %q, want canonical undotted host", err.Error())
	}

	errStub := &recordingResolver{err: errors.New("boom")}
	dErr := &Dialer{resolver: errStub}
	_, rule, err = dErr.ResolveAndCheck(ctx, "allowed.example.com", 443, nil)
	if err == nil || rule != "dns" || !strings.HasPrefix(err.Error(), "resolve allowed.example.com: ") {
		t.Fatalf("error ResolveAndCheck = (%q, %v), want dns error for canonical host", rule, err)
	}

	emptyStub := &recordingResolver{}
	dEmpty := &Dialer{resolver: emptyStub}
	_, rule, err = dEmpty.ResolveAndCheck(ctx, "allowed.example.com", 443, nil)
	if err == nil || rule != "dns" || err.Error() != "resolve allowed.example.com: no addresses" {
		t.Fatalf("empty ResolveAndCheck = (%q, %v), want no addresses error for canonical host", rule, err)
	}
}

func TestDefaultResolverFallbackAlreadyRootedHostHasSingleDot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pub := netip.MustParseAddr("203.0.113.10")
	stub := &recordingResolver{addrs: []netip.Addr{pub}}
	d := &Dialer{resolver: stub}

	addrs, rule, err := d.ResolveAndCheck(ctx, "allowed.example.com.", 443, nil)
	if err != nil || rule != "" || !slices.Equal(addrs, []netip.Addr{pub}) {
		t.Fatalf("ResolveAndCheck = (%v, %q, %v), want (%v, \"\", nil)", addrs, rule, err, []netip.Addr{pub})
	}
	wantCalls := []resolverCall{{network: "ip", host: "allowed.example.com."}}
	if got := stub.recorded(); !slices.Equal(got, wantCalls) {
		t.Fatalf("resolver calls = %v, want %v without double dot", got, wantCalls)
	}
}

func TestCustomLookupReceivesCanonicalUndottedHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pub := netip.MustParseAddr("203.0.113.10")
	stub := &recordingResolver{err: errors.New("unexpected fallback resolver call")}
	var lookupHosts []string
	d := &Dialer{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			lookupHosts = append(lookupHosts, host)
			return []netip.Addr{pub}, nil
		},
		resolver: stub,
	}

	addrs, rule, err := d.ResolveAndCheck(ctx, "allowed.example.com", 443, nil)
	if err != nil || rule != "" || !slices.Equal(addrs, []netip.Addr{pub}) {
		t.Fatalf("ResolveAndCheck = (%v, %q, %v), want (%v, \"\", nil)", addrs, rule, err, []netip.Addr{pub})
	}
	if want := []string{"allowed.example.com"}; !slices.Equal(lookupHosts, want) {
		t.Fatalf("custom Lookup hosts = %v, want %v", lookupHosts, want)
	}
	if got := stub.recorded(); len(got) != 0 {
		t.Fatalf("fallback resolver called when Lookup != nil: %v", got)
	}

	// Literal IP targets bypass DNS resolution entirely.
	dLiteral := &Dialer{resolver: stub}
	addrs, rule, err = dLiteral.ResolveAndCheck(ctx, pub.String(), 443, nil)
	if err != nil || rule != "" || !slices.Equal(addrs, []netip.Addr{pub}) {
		t.Fatalf("literal ResolveAndCheck = (%v, %q, %v), want (%v, \"\", nil)", addrs, rule, err, []netip.Addr{pub})
	}
	if got := stub.recorded(); len(got) != 0 {
		t.Fatalf("fallback resolver called for literal IP: %v", got)
	}
}

type dnsQuestion struct {
	name  string
	qtype uint16
}

type dnsReply struct {
	rcode uint16
	ipv4  netip.Addr
}

type pipeDNSFixture struct {
	mu        sync.Mutex
	closed    bool
	conns     []net.Conn
	questions []dnsQuestion
	wg        sync.WaitGroup
	reply     func(q dnsQuestion) dnsReply
}

func newPipeDNSFixture(t *testing.T, reply func(q dnsQuestion) dnsReply) *pipeDNSFixture {
	t.Helper()
	f := &pipeDNSFixture{reply: reply}
	t.Cleanup(func() {
		f.mu.Lock()
		f.closed = true
		conns := f.conns
		f.conns = nil
		f.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
		f.wg.Wait()
	})
	return f
}

func (f *pipeDNSFixture) resolver() *net.Resolver {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return nil, net.ErrClosed
		}
		client, server := net.Pipe()
		f.conns = append(f.conns, client, server)
		f.wg.Add(1)
		f.mu.Unlock()

		deadline := time.Now().Add(5 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = server.SetDeadline(deadline)

		go func() {
			defer f.wg.Done()
			defer func() { _ = server.Close() }()
			f.serveConn(server)
		}()
		return client, nil
	}
	return &net.Resolver{PreferGo: true, Dial: dial}
}

func (f *pipeDNSFixture) serveConn(server net.Conn) {
	var prefix [2]byte
	if _, err := io.ReadFull(server, prefix[:]); err != nil {
		return
	}
	msgLen := int(binary.BigEndian.Uint16(prefix[:]))
	if msgLen < 12 {
		return
	}
	packet := make([]byte, msgLen)
	if _, err := io.ReadFull(server, packet); err != nil {
		return
	}
	if binary.BigEndian.Uint16(packet[4:6]) != 1 {
		return
	}
	cursor := 12
	var labels []string
	for {
		if cursor >= len(packet) {
			return
		}
		n := int(packet[cursor])
		cursor++
		if n == 0 {
			break
		}
		if n > 63 || cursor+n > len(packet) {
			return
		}
		labels = append(labels, string(packet[cursor:cursor+n]))
		cursor += n
	}
	if cursor+4 > len(packet) {
		return
	}
	qtype := binary.BigEndian.Uint16(packet[cursor : cursor+2])
	qclass := binary.BigEndian.Uint16(packet[cursor+2 : cursor+4])
	if qclass != dnsClassINET {
		return
	}
	name := "."
	if len(labels) > 0 {
		name = strings.Join(labels, ".") + "."
	}
	q := dnsQuestion{name: name, qtype: qtype}
	f.mu.Lock()
	f.questions = append(f.questions, q)
	f.mu.Unlock()

	rep := f.reply(q)
	answer := make([]byte, cursor+4, cursor+4+16)
	copy(answer, packet[:cursor+4])
	flags := dnsFlagsResponse | rep.rcode
	binary.BigEndian.PutUint16(answer[2:4], flags)
	clear(answer[6:12])
	if rep.rcode == 0 && rep.ipv4.Is4() && qtype == dnsTypeA {
		ip4 := rep.ipv4.As4()
		binary.BigEndian.PutUint16(answer[6:8], 1)
		answer = append(answer,
			0xc0, 0x0c,
			0x00, 0x01,
			0x00, 0x01,
			0x00, 0x00, 0x00, 0x01,
			0x00, 0x04,
			ip4[0], ip4[1], ip4[2], ip4[3],
		)
	}
	if len(answer) > 0xffff {
		return
	}
	frame := make([]byte, 2+len(answer))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(answer)))
	copy(frame[2:], answer)
	_, _ = server.Write(frame)
}

func (f *pipeDNSFixture) recordedQuestions() []dnsQuestion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.questions)
}

func assertExactFQDNQuestions(t *testing.T, got []dnsQuestion, wantFQDN string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("resolver issued no DNS wire questions, want %q", wantFQDN)
	}
	var sawA, sawAAAA bool
	for _, q := range got {
		if q.name != wantFQDN {
			t.Errorf("ungranted DNS wire question %q (qtype=%d), want only %q", q.name, q.qtype, wantFQDN)
		}
		switch q.qtype {
		case dnsTypeA:
			sawA = true
		case dnsTypeAAAA:
			sawAAAA = true
		default:
			t.Errorf("unexpected DNS qtype %d for %q", q.qtype, q.name)
		}
	}
	if !sawA || !sawAAAA {
		t.Errorf("recorded qtypes for %q: sawA=%v sawAAAA=%v, want both A and AAAA", wantFQDN, sawA, sawAAAA)
	}
}

func TestPureGoResolverWireAAAANodata(t *testing.T) {
	const wantFQDN = "allowed.example.com."
	pub := netip.MustParseAddr("203.0.113.10")

	for _, input := range []string{"allowed.example.com", "allowed.example.com."} {
		t.Run(input, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			fixture := newPipeDNSFixture(t, func(q dnsQuestion) dnsReply {
				if q.name != wantFQDN {
					return dnsReply{rcode: dnsRcodeNXDomain}
				}
				if q.qtype == dnsTypeA {
					return dnsReply{ipv4: pub}
				}
				// AAAA returns NOERROR with zero answers (NODATA).
				return dnsReply{}
			})
			d := &Dialer{resolver: fixture.resolver()}

			addrs, rule, err := d.ResolveAndCheck(ctx, input, 443, nil)
			if err != nil || rule != "" || !slices.Equal(addrs, []netip.Addr{pub}) {
				t.Fatalf("ResolveAndCheck(%q) = (%v, %q, %v), want (%v, \"\", nil)", input, addrs, rule, err, []netip.Addr{pub})
			}
			assertExactFQDNQuestions(t, fixture.recordedQuestions(), wantFQDN)
		})
	}
}

func TestPureGoResolverWireNXDomainAndNodata(t *testing.T) {
	// The resolver reads the platform's resolv.conf. A non-empty search list
	// makes these wire assertions detect search suffix expansion too. The
	// rooted-name stub test catches a missing trailing dot on every platform.
	t.Run("nxdomain", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fixture := newPipeDNSFixture(t, func(_ dnsQuestion) dnsReply {
			return dnsReply{rcode: dnsRcodeNXDomain}
		})
		d := &Dialer{resolver: fixture.resolver()}

		_, rule, err := d.ResolveAndCheck(ctx, "missing.example.com", 443, nil)
		if err == nil || rule != "dns" || !strings.HasPrefix(err.Error(), "resolve missing.example.com: ") {
			t.Fatalf("NXDOMAIN ResolveAndCheck = (%q, %v), want dns error for missing.example.com", rule, err)
		}
		assertExactFQDNQuestions(t, fixture.recordedQuestions(), "missing.example.com.")
	})

	t.Run("nodata", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fixture := newPipeDNSFixture(t, func(_ dnsQuestion) dnsReply {
			return dnsReply{}
		})
		d := &Dialer{resolver: fixture.resolver()}

		_, rule, err := d.ResolveAndCheck(ctx, "nodata.example.com", 443, nil)
		if err == nil || rule != "dns" || !strings.HasPrefix(err.Error(), "resolve nodata.example.com: ") {
			t.Fatalf("NODATA ResolveAndCheck = (%q, %v), want dns error for nodata.example.com", rule, err)
		}
		assertExactFQDNQuestions(t, fixture.recordedQuestions(), "nodata.example.com.")
	})
}

func TestPureGoResolverWireRefusesBlockedAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const wantFQDN = "metadata.example.com."
	meta := netip.MustParseAddr("169.254.169.254")
	fixture := newPipeDNSFixture(t, func(q dnsQuestion) dnsReply {
		if q.name != wantFQDN {
			return dnsReply{rcode: dnsRcodeNXDomain}
		}
		if q.qtype == dnsTypeA {
			return dnsReply{ipv4: meta}
		}
		return dnsReply{}
	})
	d := &Dialer{resolver: fixture.resolver()}

	_, rule, err := d.ResolveAndCheck(ctx, "metadata.example.com", 443, nil)
	if !errors.Is(err, ErrRefusedAddr) || rule != "range:169.254.0.0/16" {
		t.Fatalf("ResolveAndCheck = (%q, %v), want range:169.254.0.0/16 and ErrRefusedAddr", rule, err)
	}
	assertExactFQDNQuestions(t, fixture.recordedQuestions(), wantFQDN)
}
