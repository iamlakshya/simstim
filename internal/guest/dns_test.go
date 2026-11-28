package guest

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
)

type countingTestResolver struct {
	calls atomic.Int32
	ips   map[string][]netip.Addr
}

func (r *countingTestResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.calls.Add(1)
	return r.ips[host], nil
}

func makeDNSQuery(t *testing.T, id uint16, name string, qtype dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  qtype,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeDNSResp(t *testing.T, raw []byte) (dnsmessage.Header, []dnsmessage.Resource) {
	t.Helper()
	var p dnsmessage.Parser
	hdr, err := p.Start(raw)
	if err != nil {
		t.Fatalf("parse DNS response: %v", err)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatalf("skip questions: %v", err)
	}
	ans, err := p.AllAnswers()
	if err != nil {
		t.Fatalf("read answers: %v", err)
	}
	return hdr, ans
}

func queryUDP(t *testing.T, addr string, raw []byte) (dnsmessage.Header, []dnsmessage.Resource) {
	t.Helper()
	conn, err := net.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial UDP %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("write UDP DNS: %v", err)
	}
	buf := make([]byte, maxGuestDNSBytes)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read UDP DNS: %v", err)
	}
	return decodeDNSResp(t, buf[:n])
}

func queryTCPConn(t *testing.T, conn net.Conn, raw []byte) (dnsmessage.Header, []dnsmessage.Resource) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(raw)))
	if _, err := conn.Write(lb[:]); err != nil {
		t.Fatalf("write TCP len: %v", err)
	}
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("write TCP query: %v", err)
	}
	if _, err := io.ReadFull(conn, lb[:]); err != nil {
		t.Fatalf("read TCP resp len: %v", err)
	}
	n := int(binary.BigEndian.Uint16(lb[:]))
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read TCP resp: %v", err)
	}
	return decodeDNSResp(t, buf)
}

// TestGuestInitDNSStubUDPAndTCPThroughLink verifies wall-init (RunConnWithOptions)
// starts UDP+TCP DNSStub, forwards queries via runner.OpenDNS over KindDNS link
// streams to egress.RunOnConn, answers allowed names, returns NXDOMAIN for
// disallowed names with zero upstream lookups, and suppresses HTTPS/SVCB.
func TestGuestInitDNSStubUDPAndTCPThroughLink(t *testing.T) {
	ap, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	res := &countingTestResolver{ips: map[string][]netip.Addr{
		"registry.npmjs.org": {netip.AddrFrom4([4]byte{104, 16, 2, 34})},
	}}
	sd := &egress.SafeDialer{Addrs: ap, Resolver: res}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Egress process side.
	egSupConn, egSrvConn := net.Pipe()
	egDone := make(chan error, 1)
	go func() {
		egDone <- egress.RunOnConn(ctx, "dns-init-e2e", egSrvConn, &egress.ServerOptions{
			Addrs:  ap,
			Dialer: sd,
		})
	}()
	egCli, err := egress.NewClient(ctx, egSupConn, egress.StartArgs{
		PolicyTOML: "session = \"box/d1\"\n[sandbox]\npreset = \"balanced\"\n",
		DNSMode:    egress.DNSModeReal,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = egCli.Close()
		<-egDone
	}()

	// Supervisor-to-guest link bridging KindDNS from guest init to egress.
	supGuestConn, guestConn := net.Pipe()
	supLink := link.New(supGuestConn, link.Supervisor, link.Options{})
	defer func() { _ = supLink.Close() }()

	var bridgeWG sync.WaitGroup
	bridgeWG.Add(1)
	go func() {
		defer bridgeWG.Done()
		for {
			st, err := supLink.Accept(ctx)
			if err != nil {
				return
			}
			if st.Kind() != link.KindDNS {
				_ = st.Close()
				continue
			}
			bridgeWG.Add(1)
			go func(gs *link.Stream) {
				defer bridgeWG.Done()
				defer func() { _ = gs.Close() }()
				up, err := egCli.OpenDNS(ctx)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(up, gs)
					_ = up.CloseWrite()
					close(done)
				}()
				_, _ = io.Copy(gs, up)
				_ = gs.CloseWrite()
				<-done
			}(st)
		}
	}()

	ready := make(chan [2]net.Addr, 1)
	initDone := make(chan error, 1)
	go func() {
		initDone <- RunConnWithOptions(guestConn, t.TempDir(), RunOptions{
			DNS:     true,
			DNSAddr: "127.0.0.1:0",
			OnDNSReady: func(udpAddr, tcpAddr net.Addr) {
				ready <- [2]net.Addr{udpAddr, tcpAddr}
			},
		})
	}()

	var addrs [2]net.Addr
	select {
	case addrs = <-ready:
	case err := <-initDone:
		t.Fatalf("wall-init exited before DNS ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for wall-init DNS stub")
	}
	if addrs[0] == nil || addrs[1] == nil {
		t.Fatalf("expected both UDP and TCP DNS addresses, got %v", addrs)
	}

	// 1. Disallowed host over UDP -> NXDOMAIN, zero upstream calls.
	hdr, ans := queryUDP(t, addrs[0].String(), makeDNSQuery(t, 1, "example.com.", dnsmessage.TypeA))
	if hdr.RCode != dnsmessage.RCodeNameError || len(ans) != 0 || res.calls.Load() != 0 {
		t.Fatalf("UDP disallowed: rcode=%v ans=%d calls=%d", hdr.RCode, len(ans), res.calls.Load())
	}

	// 2. HTTPS (65) and SVCB (64) over UDP -> empty NOERROR, zero upstream calls.
	for _, qt := range []dnsmessage.Type{65, 64} {
		hdr, ans = queryUDP(t, addrs[0].String(), makeDNSQuery(t, 2, "registry.npmjs.org.", qt))
		if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 0 || res.calls.Load() != 0 {
			t.Fatalf("UDP type %d: rcode=%v ans=%d calls=%d", qt, hdr.RCode, len(ans), res.calls.Load())
		}
	}

	// 3. Allowed host over UDP -> real IP.
	hdr, ans = queryUDP(t, addrs[0].String(), makeDNSQuery(t, 3, "registry.npmjs.org.", dnsmessage.TypeA))
	if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 1 {
		t.Fatalf("UDP allowed: rcode=%v ans=%d", hdr.RCode, len(ans))
	}

	// 4. Multiple sequential queries over ONE TCP DNS connection (RFC 1035 §4.2.2).
	tcpConn, err := net.DialTimeout("tcp", addrs[1].String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial TCP DNS %s: %v", addrs[1], err)
	}
	defer func() { _ = tcpConn.Close() }()

	callsBefore := res.calls.Load()
	hdr, ans = queryTCPConn(t, tcpConn, makeDNSQuery(t, 4, "evil.example.", dnsmessage.TypeA))
	if hdr.RCode != dnsmessage.RCodeNameError || len(ans) != 0 || res.calls.Load() != callsBefore {
		t.Fatalf("TCP disallowed: rcode=%v ans=%d calls=%d (before=%d)", hdr.RCode, len(ans), res.calls.Load(), callsBefore)
	}
	hdr, ans = queryTCPConn(t, tcpConn, makeDNSQuery(t, 5, "registry.npmjs.org.", dnsmessage.TypeA))
	if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 1 {
		t.Fatalf("TCP allowed: rcode=%v ans=%d", hdr.RCode, len(ans))
	}
	aBody, ok := ans[0].Body.(*dnsmessage.AResource)
	if !ok || netip.AddrFrom4(aBody.A) != netip.AddrFrom4([4]byte{104, 16, 2, 34}) {
		t.Errorf("TCP A answer = %+v, want 104.16.2.34", ans[0].Body)
	}

	cancel()
	_ = supLink.Close()
	<-initDone
	bridgeWG.Wait()
}
