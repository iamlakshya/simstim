package host_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestWalledPlainPortGrantAndRevoke(t *testing.T) {
	h := hosttest.New(t)

	// Start a loopback TCP echo server representing a service inside the sandbox.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	s := startWalled(t, "ports-walled", "/bin/sh", "-c", "echo READY; exec sleep 60")
	waitOutput(t, s.ID, "READY")

	c := dial(t, h)
	if !c.HasFeature("port.grant") {
		t.Fatalf("bridge lacks port.grant feature: %q", c.Features)
	}

	// Before granting, the port socket must not exist.
	sock := paths.PortSocket(s.ID, port)
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("port socket %s exists before grant: %v", sock, err)
	}

	// Grant the port: supervisor opens a private Unix socket, never a TCP port.
	res, err := c.GrantPort(context.Background(), s.ID, port)
	if err != nil {
		t.Fatalf("GrantPort: %v", err)
	}
	if res.Port != port || res.Socket != sock || res.AlreadyGranted || !res.FirstForward {
		t.Fatalf("first GrantPort = %+v, want socket=%q first=true", res, sock)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("port socket %s: fi=%v err=%v, want mode 0600", sock, fi, err)
	}
	if fi, err := os.Stat(paths.PortDir(s.ID)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("port dir: fi=%v err=%v, want mode 0700", fi, err)
	}

	// Supervisor must hold zero TCP/UDP sockets even while a port is granted.
	m := live(t, s.ID)
	if runtime.GOOS == "linux" {
		assertSupervisorNoNetworkSockets(t, m.Run.HolderPID)
	}

	// Session row shows the granted port, and ports lists it as granted.
	list, err := c.Sessions(context.Background())
	if err != nil || len(list) != 1 || !slices.Equal(list[0].Ports, []int{port}) {
		t.Fatalf("sessions after grant = %+v, err=%v, want Ports=[%d]", list, err, port)
	}
	ports, err := c.Ports(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("Ports: %v", err)
	}
	foundGranted := false
	for _, p := range ports {
		if p.Port == port && p.Granted && p.Socket == sock {
			foundGranted = true
		}
	}
	if !foundGranted {
		t.Fatalf("Ports after grant = %+v, want port %d granted with socket %q", ports, port, sock)
	}

	// Traffic through the host Unix socket reaches the guest loopback TCP server.
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(context.Background(), "unix", sock)
	if err != nil {
		t.Fatalf("dial granted port socket: %v", err)
	}
	const msg = "ping-over-port-socket"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	got, err := io.ReadAll(conn)
	_ = conn.Close()
	if err != nil || string(got) != msg {
		t.Fatalf("port socket echo = %q (err=%v), want %q", got, err, msg)
	}

	// Re-granting the same port is idempotent and not marked FirstForward.
	res2, err := c.GrantPort(context.Background(), s.ID, port)
	if err != nil || !res2.AlreadyGranted || res2.FirstForward || res2.Socket != sock {
		t.Fatalf("second GrantPort = %+v, %v", res2, err)
	}

	// Revoking closes and removes the Unix socket; further dials fail.
	if err := c.RevokePort(context.Background(), s.ID, port); err != nil {
		t.Fatalf("RevokePort: %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("port socket %s still exists after revoke: %v", sock, err)
	}
	if conn2, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "unix", sock); err == nil {
		_ = conn2.Close()
		t.Fatal("dial succeeded after revoke")
	}

	// Audit log recorded grant, connect and revoke.
	audit, err := os.ReadFile(paths.AuditLog(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{`"port_grant"`, `"port_connect"`, `"port_revoke"`} {
		if !strings.Contains(string(audit), ev) {
			t.Errorf("audit log missing %s:\n%s", ev, audit)
		}
	}

	// Unwalled session rejects port.grant cleanly.
	unwalled := start(t, "unwalled-port", "sleep", "60")
	if _, err := c.GrantPort(context.Background(), unwalled.ID, port); err == nil {
		t.Fatal("GrantPort on unwalled session succeeded, want error")
	}
}

func TestWalledPlainPortGrantRefusedInsideSession(t *testing.T) {
	h := hosttest.New(t)
	s := startWalled(t, "inside-port", "sleep", "60")
	hosttest.InSession(t, s.ID)
	c := dial(t, h)
	if _, err := c.GrantPort(context.Background(), s.ID, 3000); err == nil || !strings.Contains(err.Error(), "inside a session") {
		t.Fatalf("GrantPort from inside session = %v, want forbidden", err)
	}
}

func assertSupervisorNoNetworkSockets(t *testing.T, pid int) {
	t.Helper()
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		t.Fatalf("read supervisor fds: %v", err)
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if err != nil {
			continue
		}
		if rest, ok := strings.CutPrefix(target, "socket:["); ok {
			inodes[strings.TrimSuffix(rest, "]")] = true
		}
	}
	for _, netFile := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"} {
		b, err := os.ReadFile(netFile)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) >= 10 && inodes[fields[9]] {
				t.Errorf("supervisor holds a network socket in %s: %s", netFile, line)
			}
		}
	}
}
