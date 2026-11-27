//go:build linux

package guest

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/link"
)

// TestLinuxNetnsPort53InheritanceWithoutCapabilities verifies the launcher FD
// contract (DNSFDSEnv=4,5 bound to 127.0.0.53:53 UDP and TCP) inside an
// unprivileged Linux user+network namespace when the host kernel permits one,
// This fixture does not exercise the production cmd/userns launcher. Its
// process-wall getent and capability checks are separate integration gates.
func TestLinuxNetnsPort53InheritanceWithoutCapabilities(t *testing.T) {
	if os.Getenv("TEST_GUEST_NETNS_STAGE") == "init" {
		runLinuxNetnsInitStage(t)
		return
	}
	if os.Getenv("TEST_GUEST_NETNS_STAGE") == "launcher" {
		runLinuxNetnsLauncherStage(t)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), exe, "-test.run=^TestLinuxNetnsPort53InheritanceWithoutCapabilities$")
	cmd.Env = append(os.Environ(), "TEST_GUEST_NETNS_STAGE=launcher")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getuid(),
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getgid(),
			Size:        1,
		}},
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "operation not permitted") || strings.Contains(err.Error(), "operation not permitted") || strings.Contains(err.Error(), "permission denied") {
			t.Skipf("kernel blocks unprivileged CLONE_NEWUSER|CLONE_NEWNET here (%v); production launcher validation remains separate", err)
		}
		t.Fatalf("netns launcher stage failed (%v):\n%s", err, out)
	}
}

func runLinuxNetnsLauncherStage(t *testing.T) {
	t.Helper()
	// Bring up loopback in the isolated netns via ioctl SIOCSIFFLAGS.
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ifr [40]byte
	copy(ifr[:], "lo")
	*(*uint16)(unsafeIffFlags(&ifr)) = syscall.IFF_UP | syscall.IFF_RUNNING
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.SIOCSIFFLAGS), uintptr(unsafeIfrPtr(&ifr)))
	_ = syscall.Close(fd)
	if errno != 0 {
		t.Fatalf("bring up lo in netns: %v", errno)
	}

	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 53), Port: 53})
	if err != nil {
		t.Fatalf("bind UDP 127.0.0.53:53: %v", err)
	}
	uf, err := udpConn.File()
	_ = udpConn.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer uf.Close()

	tcpLn, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 53), Port: 53})
	if err != nil {
		t.Fatalf("bind TCP 127.0.0.53:53: %v", err)
	}
	tf, err := tcpLn.File()
	_ = tcpLn.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer tf.Close()

	supConn, guestLink, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	defer supConn.Close()
	defer guestLink.Close()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Spawn an unprivileged uid inside a nested userns so CapEff and CapPrm are 0
	// when the child execs exe without file capabilities.
	child := exec.CommandContext(context.Background(), exe, "-test.run=^TestLinuxNetnsPort53InheritanceWithoutCapabilities$")
	child.Env = append(os.Environ(), "TEST_GUEST_NETNS_STAGE=init", DNSFDSEnv+"=4,5")
	child.ExtraFiles = []*os.File{guestLink, uf, tf} // fd 3 = link, fd 4 = UDP :53, fd 5 = TCP :53
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: 1000,
			HostID:      0,
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: 1000,
			HostID:      0,
			Size:        1,
		}},
	}
	if err := child.Start(); err != nil {
		t.Fatalf("start capability-free init stage: %v", err)
	}
	_ = guestLink.Close()
	_ = uf.Close()
	_ = tf.Close()

	sup := link.New(supConn, link.Supervisor, link.Options{})
	go func() {
		st, err := sup.Accept(context.Background())
		if err != nil {
			return
		}
		defer st.Close()
		var p dnsmessage.Parser
		buf := make([]byte, 512)
		n, _ := st.Read(buf)
		hdr, err := p.Start(buf[:n])
		if err != nil {
			return
		}
		q, err := p.Question()
		if err != nil {
			return
		}
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
			ID: hdr.ID, Response: true, RecursionDesired: hdr.RecursionDesired, RecursionAvailable: true, RCode: dnsmessage.RCodeNameError,
		})
		_ = b.StartQuestions()
		_ = b.Question(q)
		pkt, _ := b.Finish()
		_, _ = st.Write(pkt)
		_ = st.CloseWrite()
	}()

	if err := child.Wait(); err != nil {
		_ = sup.Close()
		t.Fatalf("capability-free init stage failed: %v", err)
	}
	_ = sup.Close()
}

func runLinuxNetnsInitStage(t *testing.T) {
	t.Helper()
	statusBytes, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(statusBytes), "\n") {
		if strings.HasPrefix(line, "CapEff:") || strings.HasPrefix(line, "CapPrm:") {
			fields := strings.Fields(line)
			if len(fields) == 2 && strings.TrimLeft(fields[1], "0") != "" {
				t.Fatalf("expected zero capability in init stage, got %s", line)
			}
		}
	}

	socks, err := claimInheritedDNSSockets()
	if err != nil {
		t.Fatalf("claimInheritedDNSSockets on 127.0.0.53:53 failed: %v", err)
	}
	f := os.NewFile(3, "link")
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		_ = socks.Close()
		t.Fatal(err)
	}
	sess := link.New(conn, link.Guest, link.Options{})
	defer sess.Close()
	stub, err := StartDNS(sess, socks, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stub.Close()

	msg := sendUDPDNSQuery(t, "127.0.0.53:53", buildGuestDNSQuery(t, 0x7777, "example.com.", dnsmessage.TypeA, 0))
	if msg.Header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("127.0.0.53:53 UDP reply rcode = %v, want NXDOMAIN", msg.Header.RCode)
	}
	time.Sleep(10 * time.Millisecond)
}

func unsafeIffFlags(ifr *[40]byte) unsafe.Pointer { return unsafe.Pointer(&ifr[16]) }
func unsafeIfrPtr(ifr *[40]byte) unsafe.Pointer   { return unsafe.Pointer(&ifr[0]) }
