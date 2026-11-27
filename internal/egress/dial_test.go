package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestRefusedRangesAndTransitions(t *testing.T) {
	hostIPv4 := netip.MustParseAddr("198.51.100.42")
	hostIPv6 := netip.MustParseAddr("2001:db8::42")
	d := &Dialer{HostAddrs: []netip.Addr{hostIPv4, hostIPv6}}

	refused := []netip.Addr{
		netip.MustParseAddr("0.0.0.0"),
		netip.AddrFrom4([4]byte{10, 0, 0, 1}),
		netip.AddrFrom4([4]byte{100, 64, 0, 1}),
		netip.MustParseAddr("127.0.0.1"),
		netip.MustParseAddr("169.254.169.254"),
		netip.AddrFrom4([4]byte{172, 16, 0, 1}),
		netip.MustParseAddr("192.0.0.1"),
		netip.AddrFrom4([4]byte{192, 168, 1, 1}),
		netip.MustParseAddr("198.18.0.1"),
		netip.MustParseAddr("224.0.0.1"),
		netip.MustParseAddr("255.255.255.255"),
		netip.MustParseAddr("::"),
		netip.MustParseAddr("::1"),
		netip.MustParseAddr("64:ff9b:1::1"),
		netip.MustParseAddr("2001::1"),
		netip.MustParseAddr("2002::1"),
		netip.MustParseAddr("fc00::1"),
		netip.MustParseAddr("fd00:ec2::254"),
		netip.MustParseAddr("fe80::1"),
		netip.MustParseAddr("ff02::1"),
		// IPv4-in-IPv6 transition/translation forms carrying metadata 169.254.169.254:
		netip.MustParseAddr("::ffff:169.254.169.254"),
		netip.MustParseAddr("::169.254.169.254"),
		netip.MustParseAddr("::ffff:0:169.254.169.254"),
		netip.MustParseAddr("64:ff9b::a9fe:a9fe"),
		netip.MustParseAddr("64:ff9b:1::a9fe:a9fe"),
		netip.MustParseAddr("2002:a9fe:a9fe::1"),
		// Teredo with public server 203.0.113.10 and XOR-encoded client 169.254.169.254 (^0xa9fe = 0x5601):
		netip.MustParseAddr("2001:0000:cb00:710a:0000:0000:5601:5601"),
		// Host's own interface addresses (and IPv4-mapped form):
		hostIPv4,
		hostIPv6,
		netip.MustParseAddr("::ffff:198.51.100.42"),
	}
	for _, addr := range refused {
		if rule, ok := d.CheckAddr(addr, 443, nil); ok || rule == "" {
			t.Errorf("CheckAddr(%s) = (%q, %v), want refused", addr, rule, ok)
		}
	}

	// Public IPv4, public IPv6, and well-known NAT64 carrying public 203.0.113.10 pass.
	allowed := []netip.Addr{
		netip.MustParseAddr("203.0.113.10"),
		netip.MustParseAddr("2606:4700:4700::1111"),
		netip.MustParseAddr("64:ff9b::cb00:710a"),
	}
	for _, addr := range allowed {
		if rule, ok := d.CheckAddr(addr, 443, nil); !ok {
			t.Errorf("CheckAddr(%s) = (%q, false), want allowed", addr, rule)
		}
	}
}

func TestHostnameGrantNeverOpensRefusedRangeOnlyNumericIPGrantDoes(t *testing.T) {
	privAddr := netip.AddrFrom4([4]byte{10, 2, 3, 4})
	d := &Dialer{
		Lookup: func(_ context.Context, _ string) ([]netip.Addr, error) {
			return []netip.Addr{privAddr}, nil
		},
	}

	// Hostname allow rule does NOT make AllowsAddr true for 10/8.
	polHost, _, err := MergeLayers([]api.Layer{{
		Kind:  api.LayerSession,
		Rules: []api.Rule{{Allow: true, Door: api.DoorNet, Target: "internal.example.com"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	engHost, err := NewEngine("shell", polHost)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ResolveAndCheck(context.Background(), "internal.example.com", 443, engHost.AllowsAddr); !errors.Is(err, ErrRefusedAddr) {
		t.Fatalf("hostname resolving to 10/8 got err = %v, want ErrRefusedAddr", err)
	}

	// Explicit numeric CIDR allow DOES open that range on its port.
	polCIDR, _, err := MergeLayers([]api.Layer{{
		Kind:  api.LayerSession,
		Rules: []api.Rule{{Allow: true, Door: api.DoorNet, Target: "10/8:8443"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	engCIDR, err := NewEngine("shell", polCIDR)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ResolveAndCheck(context.Background(), privAddr.String(), 8443, engCIDR.AllowsAddr); err != nil {
		t.Fatalf("explicit 10/8:8443 allow failed: %v", err)
	}
	if _, _, err := d.ResolveAndCheck(context.Background(), privAddr.String(), 443, engCIDR.AllowsAddr); !errors.Is(err, ErrRefusedAddr) {
		t.Fatalf("10/8:8443 allow opened port 443: %v", err)
	}
}

func TestDNSFlipNeverDialsMetadata(t *testing.T) {
	var lookups atomic.Int32
	var dialled []netip.AddrPort

	pub := netip.MustParseAddr("203.0.113.10")
	meta := netip.MustParseAddr("169.254.169.254")

	d := &Dialer{
		Lookup: func(_ context.Context, _ string) ([]netip.Addr, error) {
			if lookups.Add(1) == 1 {
				return []netip.Addr{pub}, nil
			}
			return []netip.Addr{meta}, nil
		},
		DialTCP: func(_ context.Context, ap netip.AddrPort) (net.Conn, error) {
			dialled = append(dialled, ap)
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return c1, nil
		},
	}

	// First Dial resolves once to 203.0.113.10 and dials that literal IP:port
	// directly without triggering a second DNS lookup.
	conn, ip, _, err := d.Dial(context.Background(), "rebind.example.com", 443, nil)
	if err != nil {
		t.Fatalf("first Dial failed: %v", err)
	}
	_ = conn.Close()
	if ip != pub || lookups.Load() != 1 {
		t.Fatalf("after first Dial: ip=%s lookups=%d, want %s and 1", ip, lookups.Load(), pub)
	}

	// Second Dial gets 169.254.169.254 from DNS and refuses before dialling.
	_, _, rule, err := d.Dial(context.Background(), "rebind.example.com", 443, nil)
	if !errors.Is(err, ErrRefusedAddr) {
		t.Fatalf("second Dial err = %v, want ErrRefusedAddr", err)
	}
	if rule != "range:169.254.0.0/16" {
		t.Errorf("second Dial rule = %q, want range:169.254.0.0/16", rule)
	}
	if len(dialled) != 1 || dialled[0] != netip.AddrPortFrom(pub, 443) {
		t.Fatalf("dialled targets = %v, metadata must never be dialled", dialled)
	}

	// A single lookup returning both a public IP and metadata is also refused.
	dMixed := &Dialer{
		Lookup: func(_ context.Context, _ string) ([]netip.Addr, error) {
			return []netip.Addr{pub, meta}, nil
		},
		DialTCP: func(_ context.Context, ap netip.AddrPort) (net.Conn, error) {
			t.Fatalf("unexpected dial to %s on mixed DNS response", ap)
			return nil, nil
		},
	}
	if _, _, _, err := dMixed.Dial(context.Background(), "mixed.example.com", 443, nil); !errors.Is(err, ErrRefusedAddr) {
		t.Fatalf("mixed DNS response err = %v, want ErrRefusedAddr", err)
	}
}
