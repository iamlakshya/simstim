//go:build linux

package guest

import (
	"context"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
)

// TestProcessWallGetentFailsClosedWithoutDNSStub verifies that inside a
// bubblewrap process wall (--unshare-net + empty synthetic /etc/resolv.conf),
// `getent hosts example.com` fails closed with no DNS traffic leaving the host.
func TestProcessWallGetentFailsClosedWithoutDNSStub(t *testing.T) {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap not installed")
	}
	getentPath, err := exec.LookPath("getent")
	if err != nil {
		t.Skip("getent not installed")
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer probeCancel()
	if out, err := exec.CommandContext(probeCtx, bwrapPath, "--unshare-user", "--unshare-pid", "--unshare-net", "--ro-bind", "/", "/", "--", "/bin/true").CombinedOutput(); err != nil {
		t.Skipf("bwrap sandbox unavailable here: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	resolvPath := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(resolvPath, []byte("# Direct DNS is disabled in this sandbox; use the HTTP proxy.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostsPath := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n::1 localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-net",
		"--uid", "1000", "--gid", "1000", "--cap-drop", "ALL", "--die-with-parent",
		"--proc", "/proc", "--dev", "/dev",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/etc", "/etc",
		"--ro-bind", resolvPath, "/etc/resolv.conf",
		"--ro-bind", hostsPath, "/etc/hosts",
		"--tmpfs", "/tmp",
		"--clearenv",
		"--setenv", "PATH", "/usr/bin:/bin",
	}
	for _, dir := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		if fi, serr := os.Lstat(dir); serr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				if tgt, rerr := os.Readlink(dir); rerr == nil {
					args = append(args, "--symlink", tgt, dir)
				}
			} else if fi.IsDir() {
				args = append(args, "--ro-bind", dir, dir)
			}
		}
	}
	args = append(args, "--", getentPath, "hosts", "example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bwrapPath, args...).CombinedOutput()
	if err == nil {
		t.Fatalf("getent hosts example.com unexpectedly succeeded inside process wall: %s", string(out))
	}
}

// TestLiveHostADNSPolicyInBwrap is an opt-in live proof run on host-a
// (<BRAND>_LIVE_HOST_TEST=1). It verifies both process-wall fail-closed
// `getent hosts example.com` inside bwrap and UDP+TCP DNS policy over
// guest.DNSStub -> egress (real + synthetic unknown=ask vs unknown=deny).
func TestLiveHostADNSPolicyInBwrap(t *testing.T) {
	if os.Getenv(brand.Env("LIVE_HOST_TEST")) != "1" {
		t.Skip("set " + brand.Env("LIVE_HOST_TEST") + "=1 to run the live host-a DNS proof")
	}

	// 1. Real bwrap process wall: getent hosts example.com fails closed.
	TestProcessWallGetentFailsClosedWithoutDNSStub(t)

	// 2. Real guest.DNSStub (UDP+TCP) -> egress.RunOnConn in real mode.
	TestGuestInitDNSStubUDPAndTCPThroughLink(t)

	// 3. Synthetic mode under unknown=ask vs unknown=deny over guest.DNSStub -> egress.
	ap, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	res := &countingTestResolver{}
	sd := &egress.SafeDialer{Addrs: ap, Resolver: res}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	supConn, egConn := net.Pipe()
	egDone := make(chan error, 1)
	go func() {
		egDone <- egress.RunOnConn(ctx, "host-a-dns-synth", egConn, &egress.ServerOptions{
			Addrs:  ap,
			Dialer: sd,
		})
	}()
	cli, err := egress.NewClient(ctx, supConn, egress.StartArgs{
		PolicyTOML: "session = \"host-a/dns\"\n[sandbox]\npreset = \"locked\"\n[network]\nunknown = \"ask\"\ndeny = [\"blocked.example\"]\n",
		DNSMode:    egress.DNSModeSynthetic,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = cli.Close()
		<-egDone
	}()

	stub, err := ListenDNS(ctx, cli, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go stub.Serve(ctx)
	defer func() { _ = stub.Close() }()

	// Under unknown=ask in synthetic mode: unknown gets 198.18.0.0/15 with 0 upstream calls.
	hdr, ans := queryUDP(t, stub.Addr().String(), makeDNSQuery(t, 21, "mystery.example.", dnsmessage.TypeA))
	if hdr.RCode != dnsmessage.RCodeSuccess || len(ans) != 1 || res.calls.Load() != 0 {
		t.Fatalf("host-a synthetic ask: rcode=%v ans=%d calls=%d", hdr.RCode, len(ans), res.calls.Load())
	}
	synthIP := netip.AddrFrom4(ans[0].Body.(*dnsmessage.AResource).A)
	if !netip.MustParsePrefix("198.18.0.0/15").Contains(synthIP) {
		t.Fatalf("synthetic IP %s outside 198.18.0.0/15", synthIP)
	}

	// Explicit deny gets NXDOMAIN with 0 upstream calls.
	hdr, ans = queryUDP(t, stub.Addr().String(), makeDNSQuery(t, 22, "blocked.example.", dnsmessage.TypeA))
	if hdr.RCode != dnsmessage.RCodeNameError || len(ans) != 0 || res.calls.Load() != 0 {
		t.Fatalf("host-a synthetic denied: rcode=%v ans=%d calls=%d", hdr.RCode, len(ans), res.calls.Load())
	}

	// Reload to unknown=deny: unknown gets NXDOMAIN over TCP with 0 upstream calls.
	if err := cli.Reload(ctx, "session = \"host-a/dns\"\n[sandbox]\npreset = \"locked\"\n[network]\nunknown = \"deny\"\n"); err != nil {
		t.Fatal(err)
	}
	tc, err := net.DialTimeout("tcp", stub.TCPAddr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tc.Close() }()
	hdr, ans = queryTCPConn(t, tc, makeDNSQuery(t, 23, "mystery2.example.", dnsmessage.TypeA))
	if hdr.RCode != dnsmessage.RCodeNameError || len(ans) != 0 || res.calls.Load() != 0 {
		t.Fatalf("host-a synthetic deny: rcode=%v ans=%d calls=%d", hdr.RCode, len(ans), res.calls.Load())
	}
	if !strings.HasPrefix(synthIP.String(), "198.18.") {
		t.Fatalf("unexpected synthetic IP %s", synthIP)
	}
}
