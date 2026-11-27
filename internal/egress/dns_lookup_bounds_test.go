package egress

import (
	"context"
	"net/netip"
	"slices"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/api"
)

func lookupBoundV6(n int) []netip.Addr {
	addrs := make([]netip.Addr, n)
	for i := range addrs {
		var ip [16]byte
		ip[0], ip[1], ip[2], ip[3] = 0x20, 0x01, 0x0d, 0xb8
		ip[14], ip[15] = byte(i>>8), byte(i)
		addrs[i] = netip.AddrFrom16(ip)
	}
	return addrs
}

func lookupBoundV4(n int) []netip.Addr {
	addrs := make([]netip.Addr, n)
	for i := range addrs {
		// The fixture explicitly grants this benchmark range. Its addresses
		// are never dialled, and no real resolver is called.
		addrs[i] = netip.AddrFrom4([4]byte{198, 18, byte(i >> 8), byte(i)})
	}
	return addrs
}

func TestDNSRealLookupFamilySetsAndProtocolBounds(t *testing.T) {
	v4 := netip.MustParseAddr("203.0.113.10")
	v6 := netip.MustParseAddr("2001:db8::ffff")
	v4Many, v6Many := lookupBoundV4(1500), lookupBoundV6(1500)
	cases := []struct {
		name  string
		addrs []netip.Addr
		qtype dnsmessage.Type
		rcode dnsmessage.RCode
		count int
	}{
		{"A after many AAAA", append(slices.Clone(v6Many), v4), dnsmessage.TypeA, dnsmessage.RCodeSuccess, 1},
		{"all AAAA before A", append(slices.Clone(v6Many), v4), dnsmessage.TypeAAAA, dnsmessage.RCodeSuccess, 1500},
		{"AAAA after many A", append(slices.Clone(v4Many), v6), dnsmessage.TypeAAAA, dnsmessage.RCodeSuccess, 1},
		{"all A before AAAA", append(slices.Clone(v4Many), v6), dnsmessage.TypeA, dnsmessage.RCodeSuccess, 1500},
		{"A truly absent", v6Many, dnsmessage.TypeA, dnsmessage.RCodeSuccess, 0},
		{"AAAA truly absent", v4Many, dnsmessage.TypeAAAA, dnsmessage.RCodeSuccess, 0},
		{"shared resolver input stays mapped", []netip.Addr{netip.MustParseAddr("::ffff:203.0.113.10")}, dnsmessage.TypeA, dnsmessage.RCodeSuccess, 1},
		{"late private denies A", append(slices.Clone(v6Many), netip.MustParseAddr("127.0.0.1")), dnsmessage.TypeA, dnsmessage.RCodeRefused, 0},
		{"late private denies AAAA", append(slices.Clone(v6Many), netip.MustParseAddr("127.0.0.1")), dnsmessage.TypeAAAA, dnsmessage.RCodeRefused, 0},
		{"oversized AAAA retains A", append(lookupBoundV6(2500), v4), dnsmessage.TypeA, dnsmessage.RCodeSuccess, 1},
		{"oversized AAAA fails closed", append(lookupBoundV6(2500), v4), dnsmessage.TypeAAAA, dnsmessage.RCodeServerFailure, 0},
		{"oversized A retains AAAA", append(lookupBoundV4(4100), v6), dnsmessage.TypeAAAA, dnsmessage.RCodeSuccess, 1},
		{"oversized A fails closed", append(lookupBoundV4(4100), v6), dnsmessage.TypeA, dnsmessage.RCodeServerFailure, 0},
		{"lookup input cap for A", lookupBoundV6(6436), dnsmessage.TypeA, dnsmessage.RCodeServerFailure, 0},
		{"lookup input cap for AAAA", lookupBoundV6(6436), dnsmessage.TypeAAAA, dnsmessage.RCodeServerFailure, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.addrs)
			env := startTestServer(t, &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
				return tc.addrs, nil
			}})
			env.call(t, CtlInit, InitArgs{SessionID: "abcd2345", Agent: "shell", DNS: DNSModeReal, Policy: api.Policy{
				Preset: api.PresetLocked,
				Rules: []api.Rule{
					{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "api.example.com"},
					{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "198.18.0.0/15"},
				},
			}})
			raw := buildSingleDNSQuery(t, 42, true, "api.example.com.", tc.qtype, dnsmessage.ClassINET)
			msg := unpackDNSReply(t, env.srv.AnswerDNS(context.Background(), raw))
			got := extractReplyAddrs(t, msg)
			if msg.Header.RCode != tc.rcode || len(got) != tc.count {
				t.Fatalf("rcode=%v answers=%d, want rcode=%v answers=%d", msg.Header.RCode, len(got), tc.rcode, tc.count)
			}
			if tc.count == 1500 && !slices.Equal(got, tc.addrs[:1500]) {
				t.Fatal("the complete matching family was not returned unchanged")
			}
			if tc.count == 1 {
				want := v4
				if tc.qtype == dnsmessage.TypeAAAA {
					want = v6
				}
				if got[0] != want {
					t.Fatalf("answer=%v, want %v", got[0], want)
				}
			}
			if !slices.Equal(tc.addrs, before) {
				t.Fatal("DNS lookup handling mutated the resolver's shared input slice")
			}
		})
	}
}
