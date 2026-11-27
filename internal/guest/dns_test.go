package guest

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func bindLoopbackDNSSockets(t *testing.T) DNSSockets {
	t.Helper()
	var lc net.ListenConfig
	udp, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		_ = udp.Close()
		t.Fatal(err)
	}
	return DNSSockets{UDP: udp, TCP: tcp}
}

func buildGuestDNSQuery(tb testing.TB, id uint16, qname string, qtype dnsmessage.Type, ednsSize uint16) []byte {
	tb.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		tb.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(qname),
		Type:  qtype,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		tb.Fatal(err)
	}
	if ednsSize > 0 {
		if err := b.StartAdditionals(); err != nil {
			tb.Fatal(err)
		}
		if err := b.OPTResource(dnsmessage.ResourceHeader{
			Name:  dnsmessage.MustNewName("."),
			Type:  dnsmessage.TypeOPT,
			Class: dnsmessage.Class(ednsSize),
		}, dnsmessage.OPTResource{}); err != nil {
			tb.Fatal(err)
		}
	}
	pkt, err := b.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	return pkt
}

func sendUDPDNSQuery(t *testing.T, addr string, raw []byte) dnsmessage.Message {
	t.Helper()
	conn, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(raw); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("UDP DNS read: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(buf[:n]); err != nil {
		t.Fatalf("UDP DNS unpack: %v", err)
	}
	return msg
}

func sendTCPDNSQueryOnConn(t *testing.T, conn net.Conn, raw []byte, fragment bool) dnsmessage.Message {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	frame := make([]byte, 2+len(raw))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(raw)))
	copy(frame[2:], raw)
	if fragment {
		for i := range frame {
			if _, err := conn.Write(frame[i : i+1]); err != nil {
				t.Fatalf("fragmented TCP write at %d: %v", i, err)
			}
		}
	} else if _, err := conn.Write(frame); err != nil {
		t.Fatalf("TCP write: %v", err)
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		t.Fatalf("TCP response length read: %v", err)
	}
	resp := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("TCP response payload read: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(resp); err != nil {
		t.Fatalf("TCP DNS unpack: %v", err)
	}
	return msg
}

func TestGuestDNSUDPAndTCPFramingTruncationAndHeadroom(t *testing.T) {
	// Wire guest link <-> forwarder <-> egress.Server.
	gSupConn, gGuestFile, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	gGuestConn, err := net.FileConn(gGuestFile)
	_ = gGuestFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	gSup := link.New(gSupConn, link.Supervisor, link.Options{})
	gGuest := link.New(gGuestConn, link.Guest, link.Options{})
	t.Cleanup(func() {
		_ = gSup.Close()
		_ = gGuest.Close()
	})

	eSupConn, eEgFile, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	eEgConn, err := net.FileConn(eEgFile)
	_ = eEgFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	dialer := &egress.Dialer{
		Lookup: func(_ context.Context, hostName string) ([]netip.Addr, error) {
			switch hostName {
			case "registry.npmjs.org":
				return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
			case "medium.example.com":
				// 40 IPv4 records (~670 bytes): > 512 and <= 1232.
				out := make([]netip.Addr, 40)
				for i := range out {
					out[i] = netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)})
				}
				return out, nil
			case "large.example.com":
				// 90 IPv4 records (~1470 bytes): > 1232 EDNS0 cap.
				out := make([]netip.Addr, 90)
				for i := range out {
					out[i] = netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)})
				}
				return out, nil
			default:
				return nil, fmt.Errorf("unexpected lookup %s", hostName)
			}
		},
	}
	egSrv, err := egress.NewServer(eEgConn, egress.Options{Dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	go egSrv.Serve()
	eSup := link.New(eSupConn, link.Supervisor, egress.LinkOptions())
	t.Cleanup(func() {
		_ = eSup.Close()
		_ = egSrv.Close()
	})

	// Initialise egress in real DNS mode.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctlSt, err := eSup.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	pol, _, err := egress.MergeLayers([]api.Layer{{
		Kind:   api.LayerSession,
		Preset: api.PresetBalanced,
		Rules: []api.Rule{
			{Allow: true, Door: api.DoorNet, Target: "medium.example.com"},
			{Allow: true, Door: api.DoorNet, Target: "large.example.com"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	initBytes, _ := json.Marshal(egress.InitArgs{SessionID: "abcd2345", DNS: egress.DNSModeReal, Policy: pol})
	if err := proto.NewWriter(ctlSt).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: egress.CtlInit, Args: initBytes}); err != nil {
		t.Fatal(err)
	}
	ctlReader := bufio.NewReader(ctlSt)
	for {
		f, err := proto.Read(ctlReader)
		if err != nil {
			t.Fatalf("CtlInit reply = %v, %v", f, err)
		}
		if f.Type == proto.TEvent {
			continue
		}
		if f.Type != proto.TResponse {
			t.Fatalf("CtlInit reply = %v, want response", f)
		}
		break
	}

	var dnsStreams atomic.Int64
	heldProxyGate := make(chan struct{})

	// Supervisor forwarder checking exact empty KindDNS stream header {"kind":"dns"}.
	go func() {
		for {
			st, err := gSup.Accept(context.Background())
			if err != nil {
				return
			}
			switch st.Kind() {
			case link.KindDNS:
				go func(gst *link.Stream) {
					defer gst.Close()
					var rawHdr json.RawMessage
					if gst.Header(&rawHdr) != nil || egress.ParseDNSStreamHeader(rawHdr) != nil {
						t.Errorf("invalid guest KindDNS header: %s", rawHdr)
						return
					}
					dnsStreams.Add(1)
					openCtx, openCancel := context.WithTimeout(context.Background(), 3*time.Second)
					est, err := eSup.Open(openCtx, link.KindDNS, nil)
					openCancel()
					if err != nil {
						return
					}
					defer est.Close()
					_, _ = io.Copy(est, gst)
					_ = est.CloseWrite()
					_, _ = io.Copy(gst, est)
					_ = gst.CloseWrite()
				}(st)
			case link.KindEgress:
				go func(gst *link.Stream) {
					defer gst.Close()
					<-heldProxyGate
				}(st)
			default:
				_ = st.Close()
			}
		}
	}()

	stub, err := StartDNS(gGuest, bindLoopbackDNSSockets(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer stub.Close()

	udpAddr := stub.UDPAddr().String()
	tcpAddr := stub.TCPAddr().String()

	// 1. UDP allowed & denied queries each use one KindDNS stream.
	msgAllowed := sendUDPDNSQuery(t, udpAddr, buildGuestDNSQuery(t, 0x1001, "registry.npmjs.org.", dnsmessage.TypeA, 0))
	if msgAllowed.Header.RCode != dnsmessage.RCodeSuccess || len(msgAllowed.Answers) != 1 {
		t.Fatalf("UDP allowed = rcode %v answers %d", msgAllowed.Header.RCode, len(msgAllowed.Answers))
	}
	msgDenied := sendUDPDNSQuery(t, udpAddr, buildGuestDNSQuery(t, 0x1002, "example.com.", dnsmessage.TypeA, 0))
	if msgDenied.Header.RCode != dnsmessage.RCodeNameError || len(msgDenied.Answers) != 0 {
		t.Fatalf("UDP denied = rcode %v answers %d, want NXDOMAIN", msgDenied.Header.RCode, len(msgDenied.Answers))
	}
	if dnsStreams.Load() != 2 {
		t.Fatalf("KindDNS streams opened = %d, want 2", dnsStreams.Load())
	}

	// 2. UDP 512 truncation vs EDNS0 900 fit vs EDNS0 4096 clamped to 1232 + TCP full retry.
	msgMed512 := sendUDPDNSQuery(t, udpAddr, buildGuestDNSQuery(t, 0x1003, "medium.example.com.", dnsmessage.TypeA, 0))
	if !msgMed512.Header.Truncated || len(msgMed512.Answers) != 0 {
		t.Fatalf("medium UDP without EDNS0: TC=%v answers=%d, want TC=true 0 answers", msgMed512.Header.Truncated, len(msgMed512.Answers))
	}
	msgMedEDNS := sendUDPDNSQuery(t, udpAddr, buildGuestDNSQuery(t, 0x1004, "medium.example.com.", dnsmessage.TypeA, 900))
	if msgMedEDNS.Header.Truncated || len(msgMedEDNS.Answers) != 40 {
		t.Fatalf("medium UDP with EDNS0=900: TC=%v answers=%d, want TC=false 40 answers", msgMedEDNS.Header.Truncated, len(msgMedEDNS.Answers))
	}
	msgLargeEDNS := sendUDPDNSQuery(t, udpAddr, buildGuestDNSQuery(t, 0x1005, "large.example.com.", dnsmessage.TypeA, 4096))
	if !msgLargeEDNS.Header.Truncated || len(msgLargeEDNS.Answers) != 0 {
		t.Fatalf("large UDP with EDNS0=4096 (clamped 1232): TC=%v answers=%d, want TC=true 0 answers", msgLargeEDNS.Header.Truncated, len(msgLargeEDNS.Answers))
	}

	// Sequential + byte-fragmented TCP queries on one connection return all 90 records.
	tcpConn, err := net.DialTimeout("tcp", tcpAddr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	msgLargeTCP := sendTCPDNSQueryOnConn(t, tcpConn, buildGuestDNSQuery(t, 0x1006, "large.example.com.", dnsmessage.TypeA, 0), true)
	if msgLargeTCP.Header.Truncated || len(msgLargeTCP.Answers) != 90 {
		t.Fatalf("large fragmented TCP: TC=%v answers=%d, want TC=false 90 answers", msgLargeTCP.Header.Truncated, len(msgLargeTCP.Answers))
	}
	msgSeqTCP := sendTCPDNSQueryOnConn(t, tcpConn, buildGuestDNSQuery(t, 0x1007, "registry.npmjs.org.", dnsmessage.TypeA, 0), false)
	if msgSeqTCP.Header.RCode != dnsmessage.RCodeSuccess || len(msgSeqTCP.Answers) != 1 {
		t.Fatalf("second sequential TCP query = %+v", msgSeqTCP)
	}
	_ = tcpConn.Close()

	// 3. Malformed short TCP frame (< 12 bytes) closes connection without opening KindDNS stream.
	streamsBeforeBad := dnsStreams.Load()
	badConn, err := net.DialTimeout("tcp", tcpAddr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = badConn.Write([]byte{0x00, 0x05, 1, 2, 3, 4, 5})
	_ = badConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := badConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("malformed short TCP frame left connection open")
	}
	_ = badConn.Close()
	if dnsStreams.Load() != streamsBeforeBad {
		t.Fatalf("malformed TCP frame opened KindDNS stream (%d -> %d)", streamsBeforeBad, dnsStreams.Load())
	}

	// 4. Go pure-Go net.Resolver works over UDP and falls back to TCP on TC=1.
	res := &net.Resolver{
		PreferGo: true,
		Dial: func(dctx context.Context, network, _ string) (net.Conn, error) {
			var nd net.Dialer
			if strings.HasPrefix(network, "tcp") {
				return nd.DialContext(dctx, "tcp", tcpAddr)
			}
			return nd.DialContext(dctx, "udp", udpAddr)
		},
	}
	ips, err := res.LookupNetIP(ctx, "ip4", "large.example.com")
	if err != nil || len(ips) != 90 {
		t.Fatalf("net.Resolver LookupNetIP(large.example.com) = %d IPs, err=%v; want 90 IPs", len(ips), err)
	}
	if _, err := res.LookupNetIP(ctx, "ip4", "example.com"); err == nil {
		t.Fatal("net.Resolver LookupNetIP(denied example.com) succeeded, want NXDOMAIN")
	}

	// 5. Guest stream headroom: saturate Proxy with proxyMaxConcurrent (64) stalled
	// streams on the SAME link session and verify DNS still resolves without starvation.
	px, err := StartProxy(gGuest, "http://127.0.0.1:0", slog.New(slog.DiscardHandler))
	if err != nil {
		close(heldProxyGate)
		t.Fatal(err)
	}
	defer px.Close()
	pxHostPort := strings.TrimPrefix(px.URL(), "http://")
	heldConns := make([]net.Conn, 0, proxyMaxConcurrent)
	defer func() {
		close(heldProxyGate)
		for _, hc := range heldConns {
			_ = hc.Close()
		}
	}()
	for range proxyMaxConcurrent {
		hc, err := net.DialTimeout("tcp", pxHostPort, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(hc, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\nHost: registry.npmjs.org:443\r\n\r\n")
		heldConns = append(heldConns, hc)
	}
	msgUnderLoad := sendUDPDNSQuery(t, udpAddr, buildGuestDNSQuery(t, 0x1099, "registry.npmjs.org.", dnsmessage.TypeA, 0))
	if msgUnderLoad.Header.RCode != dnsmessage.RCodeSuccess || len(msgUnderLoad.Answers) != 1 {
		t.Fatalf("DNS under saturated Proxy load = %+v, want NOERROR 1 answer", msgUnderLoad)
	}
}

func TestGuestDNSCloseDeterministicLiveness(t *testing.T) {
	supConn, guestFile, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	guestConn, err := net.FileConn(guestFile)
	_ = guestFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	sup := link.New(supConn, link.Supervisor, link.Options{})
	guestSess := link.New(guestConn, link.Guest, link.Options{})
	t.Cleanup(func() {
		_ = sup.Close()
		_ = guestSess.Close()
	})

	streamAccepted := make(chan struct{})
	remoteObservedClose := make(chan struct{})
	go func() {
		st, err := sup.Accept(context.Background())
		if err != nil {
			return
		}
		defer st.Close()
		// Read the client's raw DNS query (up to CloseWrite), then hold the stream
		// open without replying until Close() tears down open streams.
		_, _ = io.ReadAll(st)
		close(streamAccepted)
		// Wait until st is closed from the guest side (Write/SetDeadline or link wake).
		for {
			time.Sleep(10 * time.Millisecond)
			if _, err := st.Write([]byte{0}); err != nil {
				close(remoteObservedClose)
				return
			}
		}
	}()

	stub, err := StartDNS(guestSess, bindLoopbackDNSSockets(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	// Active client #1: slow partial TCP client that sent only 1 byte of the 2-byte length prefix.
	slowTCP, err := net.DialTimeout("tcp", stub.TCPAddr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer slowTCP.Close()
	if _, err := slowTCP.Write([]byte{0x00}); err != nil {
		t.Fatal(err)
	}

	// Active client #2: in-flight TCP query blocked waiting for a stalled KindDNS stream.
	inflightTCP, err := net.DialTimeout("tcp", stub.TCPAddr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer inflightTCP.Close()
	inflightDone := make(chan error, 1)
	go func() {
		raw := buildGuestDNSQuery(t, 0x2222, "stalled.example.com.", dnsmessage.TypeA, 0)
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(raw)))
		_, _ = inflightTCP.Write(append(lenBuf[:], raw...))
		_, rErr := inflightTCP.Read(make([]byte, 2))
		inflightDone <- rErr
	}()

	select {
	case <-streamAccepted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for supervisor side to accept KindDNS stream")
	}

	// Close() must deterministically unblock listeners, slowTCP, and the open
	// KindDNS stream well below dnsIOTimeout (5s). Reverting conn or stream
	// bookkeeping causes d.wg.Wait() inside Close() to block for 5s and fail here.
	closeDone := make(chan struct{})
	go func() {
		_ = stub.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(600 * time.Millisecond):
		t.Fatal("DNS.Close() blocked > 600ms; active client or open stream bookkeeping was not closed")
	}

	_ = slowTCP.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := slowTCP.Read(make([]byte, 1)); err == nil {
		t.Fatal("slow partial TCP client remained readable after Close()")
	}
	select {
	case err := <-inflightDone:
		if err == nil {
			t.Fatal("in-flight TCP client received nil error after Close()")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("in-flight TCP client did not terminate promptly after Close()")
	}
	select {
	case <-remoteObservedClose:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("open KindDNS stream was not closed on shutdown")
	}
	if c, err := net.DialTimeout("tcp", stub.TCPAddr().String(), 200*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("TCP DNS listener still accepted connections after Close()")
	}
}
