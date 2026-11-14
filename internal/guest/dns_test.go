package guest

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
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
	if f, err := proto.Read(bufio.NewReader(ctlSt)); err != nil || f.Type != proto.TResponse {
		t.Fatalf("CtlInit reply = %v, %v", f, err)
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

func TestGuestDNSSocketInheritanceCloseOnExecAndFailClosed(t *testing.T) {
	// 1. Non-loopback listener is rejected by StartDNS and closed.
	supConn, guestFile, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	guestConn, err := net.FileConn(guestFile)
	_ = guestFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	supSess := link.New(supConn, link.Supervisor, link.Options{})
	guestSess := link.New(guestConn, link.Guest, link.Options{})
	t.Cleanup(func() {
		_ = supSess.Close()
		_ = guestSess.Close()
	})

	wildUDP, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	loopTCP, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = wildUDP.Close()
		t.Fatal(err)
	}
	if _, err := StartDNS(guestSess, DNSSockets{UDP: wildUDP, TCP: loopTCP}, nil); err == nil {
		t.Fatal("StartDNS accepted 0.0.0.0 UDP socket, want loopback error")
	}

	// 2. Without DNSFDSEnv, claimInheritedDNSSockets never touches arbitrary open fds.
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rPipe.Close()
	defer wPipe.Close()
	t.Setenv(DNSFDSEnv, "")
	socksNone, err := claimInheritedDNSSockets()
	if err != nil || socksNone.UDP != nil || socksNone.TCP != nil {
		t.Fatalf("empty DNSFDSEnv returned %+v, %v", socksNone, err)
	}
	if _, err := wPipe.Write([]byte("ok")); err != nil {
		t.Fatalf("unrelated pipe broken when DNSFDSEnv unset: %v", err)
	}

	// 3. Invalid DNSFDSEnv value is cleared from env and returns *proto.Error with Hint.
	t.Setenv(DNSFDSEnv, "3,4")
	_, badEnvErr := claimInheritedDNSSockets()
	var pe *proto.Error
	if !errors.As(badEnvErr, &pe) || pe.Hint == "" {
		t.Fatalf("invalid DNSFDSEnv err = %v, want *proto.Error with Hint", badEnvErr)
	}
	if _, stillSet := os.LookupEnv(DNSFDSEnv); stillSet {
		t.Fatal("claimInheritedDNSSockets left DNSFDSEnv set in process environment")
	}

	// 4. claimDNSSocketFDs validates SO_TYPE, port check, closes raw descriptors, and marks CloseOnExec.
	rawUDP, rawTCP, _ := makeRawLoopbackSocketFDs(t)
	if _, err := claimDNSSocketFDs(rawTCP, rawUDP, 0); err == nil {
		t.Fatal("claimDNSSocketFDs accepted swapped TCP/UDP descriptors")
	}
	// Swapped call must have closed both raw descriptors.
	if _, err := syscall.GetsockoptInt(rawUDP, syscall.SOL_SOCKET, syscall.SO_TYPE); !errors.Is(err, syscall.EBADF) {
		t.Errorf("swapped rawUDP fd still open: err=%v", err)
	}

	rawUDP2, rawTCP2, _ := makeRawLoopbackSocketFDs(t)
	if _, err := claimDNSSocketFDs(rawUDP2, rawTCP2, 53); err == nil {
		t.Fatal("claimDNSSocketFDs accepted non-53 ephemeral port when wantPort=53")
	}

	rawUDP3, rawTCP3, port3 := makeRawLoopbackSocketFDs(t)
	claimed, err := claimDNSSocketFDs(rawUDP3, rawTCP3, port3)
	if err != nil {
		t.Fatalf("claimDNSSocketFDs valid loopback sockets failed: %v", err)
	}
	// Original raw descriptors must be closed after adoption.
	if _, err := syscall.GetsockoptInt(rawUDP3, syscall.SOL_SOCKET, syscall.SO_TYPE); !errors.Is(err, syscall.EBADF) {
		t.Errorf("rawUDP3 still open after claim: %v", err)
	}
	if _, err := syscall.GetsockoptInt(rawTCP3, syscall.SOL_SOCKET, syscall.SO_TYPE); !errors.Is(err, syscall.EBADF) {
		t.Errorf("rawTCP3 still open after claim: %v", err)
	}
	_ = claimed.Close()

	// 5. CtlStart fail-closed checks: missing sockets with DNS="real" fails with Hint;
	// early end() and DNS="" close unclaimed sockets; real holder+agent child sees no
	// DNSFDSEnv and no leaked DNS listening sockets.
	verifyInitDNSLifecycleAndChildFDs(t)
}

func makeRawLoopbackSocketFDs(t *testing.T) (udpFD, tcpFD, port int) {
	t.Helper()
	// Bind TCP first to grab a free loopback port, then bind UDP on the same port.
	tcpLn, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	port = tcpLn.Addr().(*net.TCPAddr).Port
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		_ = tcpLn.Close()
		// Retry once if UDP port happened to be taken.
		tcpLn2, err2 := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err2 != nil {
			t.Fatal(err2)
		}
		tcpLn = tcpLn2
		port = tcpLn.Addr().(*net.TCPAddr).Port
		udpConn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			_ = tcpLn.Close()
			t.Fatal(err)
		}
	}
	uf, err := udpConn.File()
	_ = udpConn.Close()
	if err != nil {
		_ = tcpLn.Close()
		t.Fatal(err)
	}
	tf, err := tcpLn.File()
	_ = tcpLn.Close()
	if err != nil {
		_ = uf.Close()
		t.Fatal(err)
	}
	uDup, err := syscall.Dup(int(uf.Fd()))
	_ = uf.Close()
	if err != nil {
		_ = tf.Close()
		t.Fatal(err)
	}
	tDup, err := syscall.Dup(int(tf.Fd()))
	_ = tf.Close()
	if err != nil {
		_ = syscall.Close(uDup)
		t.Fatal(err)
	}
	return uDup, tDup, port
}

func verifyInitDNSLifecycleAndChildFDs(t *testing.T) {
	t.Helper()
	newPair := func() (*link.Session, *Init, string) {
		dir, err := os.MkdirTemp("/tmp", "gdns")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		ours, theirs, err := SocketPair()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.FileConn(theirs)
		_ = theirs.Close()
		if err != nil {
			t.Fatal(err)
		}
		g := New(conn, dir, slog.New(slog.DiscardHandler))
		go g.Serve()
		sup := link.New(ours, link.Supervisor, link.Options{})
		t.Cleanup(func() {
			_ = sup.Close()
			<-g.Done()
		})
		return sup, g, dir
	}

	sendStart := func(sup *link.Session, args proto.CtlStartArgs) proto.Response {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := sup.Open(ctx, link.KindCtl, nil)
		if err != nil {
			t.Fatal(err)
		}
		rawArgs, _ := json.Marshal(args)
		if err := proto.NewWriter(st).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: proto.CtlStart, Args: rawArgs}); err != nil {
			t.Fatal(err)
		}
		r := bufio.NewReader(st)
		for {
			f, err := proto.Read(r)
			if err != nil {
				t.Fatalf("read CtlStart response: %v", err)
			}
			if f.Type == proto.TResponse {
				var resp proto.Response
				_ = json.Unmarshal(f.Payload, &resp)
				return resp
			}
		}
	}

	// Case A: DNS="real" without sockets fails closed with *proto.Error and Hint.
	supA, _, _ := newPair()
	respA := sendStart(supA, proto.CtlStartArgs{
		Session: "dnsa2345",
		Argv:    []string{"/bin/sh", "-c", "exit 0"},
		Env:     []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"},
		Cwd:     "/tmp",
		DNS:     proto.DNSReal,
	})
	if respA.OK || respA.Error == nil || respA.Error.Hint == "" {
		t.Fatalf("CtlStart DNS=real without sockets = %+v, want error with Hint", respA)
	}

	// Case B: Early link close before CtlStart closes unclaimed sockets.
	supB, gB, _ := newPair()
	socksB := bindLoopbackDNSSockets(t)
	tcpAddrB := socksB.TCP.Addr().String()
	gB.SetDNSSockets(socksB)
	_ = supB.Close()
	<-gB.Done()
	if c, err := net.DialTimeout("tcp", tcpAddrB, 200*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("early end() left unclaimed TCP DNS socket open")
	}

	// Case C: Real holder + agent child with claimed DNS sockets does NOT inherit
	// DNSFDSEnv or any socket bound to the DNS listener port.
	rawU, rawT, dnsPort := makeRawLoopbackSocketFDs(t)
	claimedSocks, err := claimDNSSocketFDs(rawU, rawT, dnsPort)
	if err != nil {
		t.Fatal(err)
	}
	supC, gC, rootC := newPair()
	gC.SetDNSSockets(claimedSocks)
	outFile := filepath.Join(rootC, "agent-check.txt")
	t.Setenv(DNSFDSEnv, "should-be-stripped")

	script := fmt.Sprintf("printf 'ENV=%%s\\n' \"$%s\" > '%s'; ls /dev/fd >> '%s' 2>/dev/null || true; sleep 2", DNSFDSEnv, outFile, outFile)
	respC := sendStart(supC, proto.CtlStartArgs{
		Session: "dnsc2345",
		Agent:   "shell",
		Argv:    []string{"/bin/sh", "-c", script},
		Env: []string{
			"PATH=/usr/bin:/bin",
			"HOME=" + host.GuestHome,
			brand.Env("HOOK_SOCK") + "=" + host.GuestHookSocket(),
			"TERM=xterm-256color",
		},
		Cwd:  "/tmp",
		Cols: 80,
		Rows: 24,
		DNS:  proto.DNSReal,
	})
	if !respC.OK {
		t.Fatalf("CtlStart with valid claimed DNS sockets failed: %+v", respC.Error)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(outFile)
		if err == nil && strings.Contains(string(b), "ENV=") {
			if strings.Contains(string(b), "should-be-stripped") {
				t.Fatalf("DNSFDSEnv leaked into holder/agent environment: %s", b)
			}
			// Verify the DNS stub is serving in wall-init while the agent runs.
			c, dErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(dnsPort)), time.Second)
			if dErr != nil {
				t.Fatalf("DNS stub TCP port %d not reachable: %v", dnsPort, dErr)
			}
			_ = c.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for agent output file %s", outFile)
}

func TestHolderEnvStripsDNSFDSEnv(t *testing.T) {
	got := holderEnv([]string{"PATH=/bin", DNSFDSEnv + "=4,5", "HOME=/bad", "XDG_RUNTIME_DIR=/bad"}, "/home/agent")
	if slices.Contains(got, DNSFDSEnv+"=4,5") {
		t.Fatalf("holderEnv retained %s: %v", DNSFDSEnv, got)
	}
}

func FuzzGuestDNSUDPLimitAndTruncate(f *testing.F) {
	f.Add(buildGuestDNSQuery(f, 1, "example.com.", dnsmessage.TypeA, 0), 512)
	f.Add(buildGuestDNSQuery(f, 2, "example.com.", dnsmessage.TypeA, 4096), 1232)
	f.Add([]byte{0, 1, 0x81, 0x80, 0, 0, 0, 0, 0, 0, 0, 0}, 512)
	f.Add([]byte{}, 512)

	f.Fuzz(func(t *testing.T, raw []byte, maxPayload int) {
		limit := udpResponseLimit(raw)
		if limit < dnsMinUDPPayload || limit > dnsMaxEDNSPayload {
			t.Fatalf("udpResponseLimit(%x) = %d outside [%d, %d]", raw, limit, dnsMinUDPPayload, dnsMaxEDNSPayload)
		}
		if len(raw) >= dnsMinWireBytes {
			if maxPayload < dnsMinUDPPayload {
				maxPayload = dnsMinUDPPayload
			}
			if maxPayload > dnsMaxEDNSPayload {
				maxPayload = dnsMaxEDNSPayload
			}
			tr := truncateUDPReply(raw, maxPayload)
			if len(tr) < dnsMinWireBytes || len(tr) > maxPayload {
				t.Fatalf("truncateUDPReply length %d outside [%d, %d]", len(tr), dnsMinWireBytes, maxPayload)
			}
			if tr[2]&0x02 == 0 {
				t.Fatalf("truncateUDPReply did not set TC bit: %x", tr[:4])
			}
		}
	})
}
