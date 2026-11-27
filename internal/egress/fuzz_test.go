package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/api"
)

func FuzzStreamHeader(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"kind":"egress","via":"explicit","host":"registry.npmjs.org","port":443}`),
		[]byte(`{"via":"transparent","host":"api.anthropic.com","port":443}`),
		[]byte(`{"via":"explicit","host":"203.0.113.10","port":8080}`),
		[]byte(`{"via":"explicit","host":"[2001:db8::1]","port":443}`),
		[]byte(`{"via":"explicit","host":"bad..host","port":443}`),
		[]byte(`{"via":"unknown","host":"example.com","port":0}`),
		[]byte(`{"via":"explicit","host":"example.com","port":443,"extra":true}`),
		[]byte(`{}{}`),
		{},
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		hdr, err := ParseStreamHeader(raw)
		if err != nil {
			return
		}
		if hdr.Via != "explicit" && hdr.Via != "transparent" {
			t.Fatalf("unexpected Via %q", hdr.Via)
		}
		if hdr.Port < 1 || hdr.Port > 65535 {
			t.Fatalf("out-of-range Port %d", hdr.Port)
		}
		norm, err := NormaliseHost(hdr.Host)
		if err != nil || norm != hdr.Host {
			t.Fatalf("parsed Host %q is not normalised (%q, %v)", hdr.Host, norm, err)
		}
	})
}

func FuzzClientHello(f *testing.F) {
	valid := captureClientHello(f, &tls.Config{ServerName: "registry.npmjs.org", InsecureSkipVerify: true})
	f.Add(valid, "registry.npmjs.org")
	f.Add([]byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x01, 0x00, 0x00, 0x01, 0x00}, "example.com")
	f.Add([]byte{0x16, 0x03, 0x03, 0xff, 0xff}, "example.com")
	f.Add([]byte("CONNECT example.com:443 HTTP/1.1\r\n\r\n"), "example.com")
	f.Add([]byte{}, "")

	f.Fuzz(func(t *testing.T, data []byte, expectedHost string) {
		ch, err := ReadClientHello(bytes.NewReader(data))
		if err != nil {
			return
		}
		if len(ch.Raw) == 0 || len(ch.Raw) > maxClientHelloBytes+5*maxClientHelloRecs {
			t.Fatalf("unexpected Raw length %d", len(ch.Raw))
		}
		_ = CheckClientHello(ch, expectedHost)
	})
}

func FuzzDNSStreamHeader(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"kind":"dns"}`),
		[]byte(`null`),
		[]byte(`{"kind":"dns","mode":"synthetic"}`),
		[]byte(`{"kind":"egress"}`),
		[]byte(`{}{}`),
		{},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, raw []byte) {
		_ = ParseDNSStreamHeader(raw)
	})
}

func FuzzDNSAnswer(f *testing.F) {
	f.Add(buildSingleDNSQuery(f, 1, true, "registry.npmjs.org.", dnsmessage.TypeA, dnsmessage.ClassINET), false)
	f.Add(buildSingleDNSQuery(f, 2, true, "example.com.", dnsmessage.TypeA, dnsmessage.ClassINET), false)
	f.Add(buildSingleDNSQuery(f, 3, true, "example.com.", dnsmessage.TypeHTTPS, dnsmessage.ClassINET), true)
	f.Add(buildSingleDNSQuery(f, 4, true, "127.0.0.1.", dnsmessage.TypeA, dnsmessage.ClassINET), true)
	f.Add(buildEDNSQuery(f, "registry.npmjs.org.", dnsmessage.TypeAAAA, ".", nil), false)
	f.Add([]byte{0x00, 0x01, 0x80, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, false)
	f.Add([]byte{0x00, 0x02, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01}, false)
	f.Add([]byte{}, true)

	polReal, _, err := MergeLayers([]api.Layer{{
		Kind:   api.LayerSession,
		Preset: api.PresetLocked,
		Rules:  []api.Rule{{Allow: true, Door: api.DoorNet, Target: "registry.npmjs.org"}},
	}})
	if err != nil {
		f.Fatal(err)
	}
	polSynth, _, err := MergeLayers([]api.Layer{{
		Kind:    api.LayerSession,
		Preset:  api.PresetBalanced,
		Unknown: api.UnknownAsk,
		Rules:   []api.Rule{{Allow: false, Door: api.DoorNet, Target: "denied.example.com"}},
	}})
	if err != nil {
		f.Fatal(err)
	}

	newFuzzServer := func(tb testing.TB, mode string, pol api.Policy, lookup func(context.Context, string) ([]netip.Addr, error)) *Server {
		tb.Helper()
		c1, c2 := net.Pipe()
		tb.Cleanup(func() {
			_ = c1.Close()
			_ = c2.Close()
		})
		srv, err := NewServer(c2, Options{Dialer: &Dialer{Lookup: lookup}})
		if err != nil {
			tb.Fatal(err)
		}
		eng, err := NewEngine("shell", pol)
		if err != nil {
			tb.Fatal(err)
		}
		srv.mu.Lock()
		srv.engine = eng
		srv.dnsMode = mode
		srv.inited = true
		srv.mu.Unlock()
		return srv
	}

	realSrv := newFuzzServer(f, DNSModeReal, polReal, func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "registry.npmjs.org" {
			panic("ungranted host reached lookup: " + host)
		}
		return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
	})
	synthSrv := newFuzzServer(f, DNSModeSynthetic, polSynth, func(_ context.Context, host string) ([]netip.Addr, error) {
		panic("synthetic DNS reached lookup: " + host)
	})
	synthPrefix := netip.MustParsePrefix("198.18.0.0/15")

	f.Fuzz(func(t *testing.T, raw []byte, synthetic bool) {
		srv := realSrv
		if synthetic {
			srv = synthSrv
		}
		resp := srv.AnswerDNS(context.Background(), raw)
		if resp == nil {
			return
		}
		if len(resp) < dnsHeaderBytes || len(resp) > maxDNSMessageBytes {
			t.Fatalf("out-of-bounds DNS reply length %d", len(resp))
		}
		if len(raw) >= dnsHeaderBytes && raw[2]&0x80 != 0 {
			t.Fatalf("replied to QR=1 response packet")
		}
		var msg dnsmessage.Message
		if err := msg.Unpack(resp); err != nil {
			t.Fatalf("unpacked invalid DNS wire response: %v", err)
		}
		if !msg.Header.Response || msg.Header.Authoritative || resp[3]&0x20 != 0 {
			t.Fatalf("unexpected response header flags: %+v (flags2=%#x)", msg.Header, resp[3])
		}
		if synthetic {
			for _, ans := range msg.Answers {
				ar, ok := ans.Body.(*dnsmessage.AResource)
				if !ok {
					t.Fatalf("synthetic mode emitted non-A answer %T", ans.Body)
				}
				ip := netip.AddrFrom4(ar.A)
				if !synthPrefix.Contains(ip) || ip == netip.MustParseAddr("198.18.0.0") || ip == SyntheticDummyAddr || ip == netip.MustParseAddr("198.19.255.255") {
					t.Fatalf("synthetic mode emitted invalid pool address %s", ip)
				}
			}
		}
	})
}
