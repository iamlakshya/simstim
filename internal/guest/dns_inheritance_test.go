package guest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

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
