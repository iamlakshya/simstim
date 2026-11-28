package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type runTestResolver map[string][]netip.Addr

func (r runTestResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

// startHangingUpstream starts a local TCP server that writes "ready\n" on each
// accepted connection and then holds the connection open until closed or the
// test ends.
func startHangingUpstream(t *testing.T) (addr string, closeAll func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_, _ = io.WriteString(conn, "ready\n")
				<-done
			}(c)
		}
	}()
	stop := func() {
		select {
		case <-done:
		default:
			close(done)
		}
		_ = lis.Close()
	}
	t.Cleanup(stop)
	return lis.Addr().String(), stop
}

func TestRunOnConnShutdownWithActiveTransparentFlow(t *testing.T) {
	for _, trigger := range []string{"cancel_ctx", "close_ctl"} {
		t.Run(trigger, func(t *testing.T) {
			upAddr, stopUp := startHangingUpstream(t)
			defer stopUp()

			ap, err := NewAddressPolicyWithHosts(nil)
			if err != nil {
				t.Fatal(err)
			}
			fakePub := netip.AddrFrom4([4]byte{93, 184, 216, 34})
			sd := &SafeDialer{
				Addrs:    ap,
				Resolver: runTestResolver{"stream.example.org": {fakePub}},
				Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, upAddr)
				},
			}

			runCtx, runCancel := context.WithCancel(context.Background())
			defer runCancel()
			supConn, egConn := net.Pipe()
			runDone := make(chan error, 1)
			go func() {
				runDone <- RunOnConn(runCtx, "t-"+trigger, egConn, &ServerOptions{
					Addrs:  ap,
					Dialer: sd,
				})
			}()

			cliCtx, cliCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cliCancel()
			cli, err := NewClient(cliCtx, supConn, StartArgs{
				PolicyTOML: "[sandbox]\npreset = \"locked\"\n[network]\nallow = [\"stream.example.org:8443\"]\n",
			}, nil)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			defer func() { _ = cli.Close() }()

			st, err := cli.OpenEgress(cliCtx, StreamHeader{
				Via:  "transparent",
				Host: "stream.example.org",
				Port: 8443,
			})
			if err != nil {
				t.Fatalf("OpenEgress: %v", err)
			}
			defer func() { _ = st.Close() }()

			line, err := bufio.NewReader(st).ReadString('\n')
			if err != nil || line != "ready\n" {
				t.Fatalf("read ready = %q, %v", line, err)
			}

			// Trigger shutdown while the transparent flow is actively spliced.
			if trigger == "cancel_ctx" {
				runCancel()
			} else {
				_ = cli.ctl.Close()
			}

			select {
			case <-runDone:
			case <-time.After(2 * time.Second):
				t.Fatalf("RunOnConn deadlocked on %s with an open transparent flow", trigger)
			}
		})
	}
}

func TestRunOnConnShutdownWithActiveConnectTunnel(t *testing.T) {
	upAddr, stopUp := startHangingUpstream(t)
	defer stopUp()

	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	fakePub := netip.AddrFrom4([4]byte{93, 184, 216, 34})
	sd := &SafeDialer{
		Addrs:    ap,
		Resolver: runTestResolver{"tunnel.example.org": {fakePub}},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upAddr)
		},
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	supConn, egConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() {
		runDone <- RunOnConn(runCtx, "connect-shutdown", egConn, &ServerOptions{
			Addrs:  ap,
			Dialer: sd,
		})
	}()

	cliCtx, cliCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cliCancel()
	cli, err := NewClient(cliCtx, supConn, StartArgs{
		PolicyTOML: "[sandbox]\npreset = \"locked\"\n[network]\nallow = [\"tunnel.example.org:8443\"]\n",
	}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = cli.Close() }()

	st, err := cli.OpenEgress(cliCtx, StreamHeader{Via: "explicit"})
	if err != nil {
		t.Fatalf("OpenEgress: %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, err := io.WriteString(st, "CONNECT tunnel.example.org:8443 HTTP/1.1\r\nHost: tunnel.example.org:8443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(st)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}
	line, err := br.ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("read tunnel ready = %q, %v", line, err)
	}

	runCancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("RunOnConn deadlocked on cancel with an open CONNECT tunnel")
	}
}

func TestCtlPolicyAllowDistinguishesLockedAndBadRule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	supConn, egConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() {
		runDone <- RunOnConn(ctx, "ctl-codes", egConn, nil)
	}()

	cli, err := NewClient(ctx, supConn, StartArgs{
		PolicyTOML: "[sandbox]\npreset = \"locked\"\n[lock]\ndeny = [\"evil.example.com\"]\n",
	}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() {
		cancel()
		_ = cli.Close()
		<-runDone
	}()

	// 1. Target covered by a lock returns code "policy_locked".
	err = cli.Allow(ctx, "evil.example.com")
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "policy_locked" {
		t.Fatalf("Allow(locked) = %v, want *proto.Error{Code: policy_locked}", err)
	}

	// 2. Public-suffix wildcard returns code "bad_rule", not "policy_locked".
	err = cli.Allow(ctx, "*.com")
	if !errors.As(err, &pe) || pe.Code != "bad_rule" {
		t.Fatalf("Allow(*.com) = %v, want *proto.Error{Code: bad_rule}", err)
	}

	// 3. Invalid port / syntax returns code "bad_rule", not "policy_locked".
	err = cli.Allow(ctx, "api.example.org:99999")
	if !errors.As(err, &pe) || pe.Code != "bad_rule" {
		t.Fatalf("Allow(bad port) = %v, want *proto.Error{Code: bad_rule}", err)
	}

	// 4. Valid unlocked target succeeds.
	if err := cli.Allow(ctx, "ok.example.org"); err != nil {
		t.Fatalf("Allow(ok.example.org) = %v, want nil", err)
	}
}
