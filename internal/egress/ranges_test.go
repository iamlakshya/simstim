package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
)

func ip4(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom4([4]byte{a, b, c, d})
}

func ip4In6Mapped(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom16([16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, a, b, c, d})
}

func ip4In6Translated(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom16([16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0, 0, a, b, c, d})
}

func ip4In6Compat(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom16([16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, a, b, c, d})
}

func nat64Addr(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom16([16]byte{0x00, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0, a, b, c, d})
}

func nat64LocalAddr(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom16([16]byte{0x00, 0x64, 0xff, 0x9b, 0x00, 0x01, 0, 0, 0, 0, 0, 0, a, b, c, d})
}

func sixToFourAddr(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom16([16]byte{0x20, 0x02, a, b, c, d, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
}

func teredoAddr(srv [4]byte, cli [4]byte) netip.Addr {
	return netip.AddrFrom16([16]byte{
		0x20, 0x01, 0x00, 0x00,
		srv[0], srv[1], srv[2], srv[3],
		0x00, 0x00, 0xff, 0xff,
		^cli[0], ^cli[1], ^cli[2], ^cli[3],
	})
}

// TestAddressPolicySmokescreenCases covers spec 7.2 and Stripe smokescreen's
// private, special, link-local, metadata, NAT64, 6to4, Teredo and host-address
// refusal cases.
func TestAddressPolicySmokescreenCases(t *testing.T) {
	hostPublicV4 := ip4(203, 0, 113, 42)
	hostPublicV6 := netip.MustParseAddr("2001:4860:4860::4242")
	ap, err := NewAddressPolicyWithHosts([]netip.Addr{hostPublicV4, hostPublicV6})
	if err != nil {
		t.Fatal(err)
	}

	allowed := []struct {
		name string
		addr netip.Addr
	}{
		{"public v4 cloudflare", ip4(1, 1, 1, 1)},
		{"public v4 google", ip4(8, 8, 8, 8)},
		{"public v4 example", ip4(93, 184, 216, 34)},
		{"just below rfc1918 172.16", ip4(172, 15, 255, 255)},
		{"just above rfc1918 172.31", ip4(172, 32, 0, 1)},
		{"just below cgnat", ip4(100, 63, 255, 255)},
		{"just above cgnat", ip4(100, 128, 0, 1)},
		{"public v6", netip.MustParseAddr("2606:4700:4700::1111")},
		{"ipv4-mapped public", ip4In6Mapped(1, 1, 1, 1)},
		{"nat64 well-known public", nat64Addr(1, 1, 1, 1)},
	}
	for _, tc := range allowed {
		if err := ap.CheckAddr(tc.addr, nil); err != nil {
			t.Errorf("CheckAddr(%s = %s) = %v, want nil", tc.name, tc.addr, err)
		}
	}

	refused := []struct {
		name string
		addr netip.Addr
	}{
		// Basic IPv4 special & private ranges.
		{"unspecified v4", ip4(0, 0, 0, 0)},
		{"this network v4", ip4(0, 1, 2, 3)},
		{"rfc1918 10/8 start", ip4(10, 0, 0, 1)},
		{"rfc1918 10/8 end", ip4(10, 255, 255, 254)},
		{"rfc1918 172.16/12 start", ip4(172, 16, 0, 1)},
		{"rfc1918 172.16/12 end", ip4(172, 31, 255, 254)},
		{"rfc1918 192.168/16", ip4(192, 168, 1, 1)},
		{"cgnat start", ip4(100, 64, 0, 1)},
		{"cgnat end", ip4(100, 127, 255, 254)},
		{"loopback v4", ip4(127, 0, 0, 1)},
		{"loopback v4 alt", ip4(127, 0, 0, 53)},
		{"link-local v4", ip4(169, 254, 0, 1)},
		{"cloud imds v4", ip4(169, 254, 169, 254)},
		{"ecs metadata v4", ip4(169, 254, 170, 2)},
		{"ietf protocol v4", ip4(192, 0, 0, 1)},
		{"benchmark/synthetic v4", ip4(198, 18, 0, 1)},
		{"benchmark/synthetic v4 upper", ip4(198, 19, 255, 254)},
		{"multicast v4", ip4(224, 0, 0, 1)},
		{"reserved v4", ip4(240, 0, 0, 1)},
		{"broadcast v4", ip4(255, 255, 255, 255)},

		// Basic IPv6 special & private ranges.
		{"unspecified v6", netip.MustParseAddr("::")},
		{"loopback v6", netip.MustParseAddr("::1")},
		{"discard v6", netip.MustParseAddr("100::1")},
		{"ula fc00", netip.MustParseAddr("fc00::1")},
		{"ula fd00", netip.MustParseAddr("fd12:3456:789a::1")},
		{"aws imds v6", netip.MustParseAddr("fd00:ec2::254")},
		{"link-local v6", netip.MustParseAddr("fe80::1")},
		{"link-local v6 with zone", netip.MustParseAddr("fe80::1%eth0")},
		{"deprecated site-local v6", netip.MustParseAddr("fec0::1")},
		{"multicast v6", netip.MustParseAddr("ff02::1")},

		// Embedded IPv4 in IPv6: mapped, translated, compatible, NAT64, 6to4, Teredo.
		{"v4-mapped loopback", ip4In6Mapped(127, 0, 0, 1)},
		{"v4-mapped metadata", ip4In6Mapped(169, 254, 169, 254)},
		{"v4-mapped rfc1918", ip4In6Mapped(10, 0, 0, 1)},
		{"v4-mapped cgnat", ip4In6Mapped(100, 64, 0, 1)},
		{"v4-mapped host own", ip4In6Mapped(203, 0, 113, 42)},
		{"v4-translated loopback", ip4In6Translated(127, 0, 0, 1)},
		{"v4-translated public", ip4In6Translated(1, 1, 1, 1)},
		{"v4-compatible loopback", ip4In6Compat(127, 0, 0, 1)},
		{"v4-compatible public", ip4In6Compat(1, 1, 1, 1)},
		{"nat64 loopback", nat64Addr(127, 0, 0, 1)},
		{"nat64 metadata", nat64Addr(169, 254, 169, 254)},
		{"nat64 rfc1918", nat64Addr(192, 168, 1, 1)},
		{"nat64 cgnat", nat64Addr(100, 64, 1, 1)},
		{"nat64 host own", nat64Addr(203, 0, 113, 42)},
		{"nat64 local-use rfc8215", nat64LocalAddr(1, 1, 1, 1)},
		{"6to4 loopback", sixToFourAddr(127, 0, 0, 1)},
		{"6to4 metadata", sixToFourAddr(169, 254, 169, 254)},
		{"6to4 rfc1918 10/8", sixToFourAddr(10, 0, 0, 1)},
		{"6to4 rfc1918 192.168/16", sixToFourAddr(192, 168, 0, 1)},
		{"6to4 host own", sixToFourAddr(203, 0, 113, 42)},
		{"6to4 public prefix", sixToFourAddr(1, 1, 1, 1)},
		{"teredo client loopback", teredoAddr([4]byte{8, 8, 8, 8}, [4]byte{127, 0, 0, 1})},
		{"teredo client metadata", teredoAddr([4]byte{8, 8, 8, 8}, [4]byte{169, 254, 169, 254})},
		{"teredo client rfc1918", teredoAddr([4]byte{8, 8, 8, 8}, [4]byte{10, 0, 0, 1})},
		{"teredo server rfc1918", teredoAddr([4]byte{192, 168, 1, 1}, [4]byte{1, 1, 1, 1})},
		{"teredo public prefix", teredoAddr([4]byte{8, 8, 8, 8}, [4]byte{1, 1, 1, 1})},

		// Host's own addresses.
		{"host own v4", hostPublicV4},
		{"host own v6", hostPublicV6},
	}
	for _, tc := range refused {
		var refusedErr *RefusedAddrError
		err := ap.CheckAddr(tc.addr, nil)
		if !errors.As(err, &refusedErr) {
			t.Errorf("CheckAddr(%s = %s) = %v, want *RefusedAddrError", tc.name, tc.addr, err)
		}
	}

	// Explicit IP grant overrides a refused range.
	if err := ap.CheckAddr(ip4(10, 1, 2, 3), func(a netip.Addr) bool {
		return a == ip4(10, 1, 2, 3)
	}); err != nil {
		t.Errorf("explicit IP grant should pass: %v", err)
	}
}

type sequenceResolver struct {
	calls   atomic.Int32
	answers [][]netip.Addr
}

func (r *sequenceResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	i := int(r.calls.Add(1)) - 1
	if i >= len(r.answers) {
		i = len(r.answers) - 1
	}
	return r.answers[i], nil
}

// TestDNSRebindingNeverDialsMetadata proves P1.7's rebinding requirement:
// a name whose first lookup gives a public address and whose second lookup
// gives the metadata address is resolved once and never dialled at metadata.
func TestDNSRebindingNeverDialsMetadata(t *testing.T) {
	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(Config{Sandbox: SandboxConfig{Preset: PresetOpen}})
	if err != nil {
		t.Fatal(err)
	}

	pub := ip4(93, 184, 216, 34)
	imds := ip4(169, 254, 169, 254)
	res := &sequenceResolver{answers: [][]netip.Addr{{pub}, {imds}}}

	var dialed []string
	sd := &SafeDialer{
		Addrs:    ap,
		Engine:   eng,
		Resolver: res,
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return c1, nil
		},
	}

	addrs, err := sd.ResolveAndCheck(context.Background(), "rebind.example", 443)
	if err != nil {
		t.Fatalf("ResolveAndCheck: %v", err)
	}
	ctx := withCheckedAddrs(context.Background(), addrs)
	conn, err := sd.DialContext(ctx, "tcp", "rebind.example:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()

	if got := res.calls.Load(); got != 1 {
		t.Errorf("resolver called %d times, want 1", got)
	}
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Errorf("dialed = %v, want [93.184.216.34:443]", dialed)
	}

	// Mixed answer containing both a public IP and metadata fails closed.
	mixedRes := &sequenceResolver{answers: [][]netip.Addr{{pub, imds}}}
	sd.Resolver = mixedRes
	if _, err := sd.ResolveAndCheck(context.Background(), "mixed.example", 443); err == nil {
		t.Fatal("mixed public+metadata resolution must fail closed")
	}
}
