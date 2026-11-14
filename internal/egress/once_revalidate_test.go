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

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

func echoPolicyDialer(t *testing.T, addr netip.Addr) *Dialer {
	t.Helper()
	return &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{addr}, nil },
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			c, peer := net.Pipe()
			t.Cleanup(func() { _ = c.Close(); _ = peer.Close() })
			go func() { defer peer.Close(); _, _ = io.Copy(peer, peer) }()
			return c, nil
		},
	}
}

func requireOpaqueEcho(t *testing.T, st *link.Stream, br *bufio.Reader) {
	t.Helper()
	if _, err := io.WriteString(st, "ping\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := br.ReadString('\n'); err != nil || line != "ping\n" {
		t.Fatalf("opaque flow = %q, %v", line, err)
	}
}

func requireFlowClosed(t *testing.T, st *link.Stream, br *bufio.Reader) {
	t.Helper()
	_ = st.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := br.ReadByte()
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("revoked flow did not close: %v", err)
	}
}

func TestOnceLiveFlowSurvivesUnrelatedReloadWithoutRearming(t *testing.T) {
	env := startTestServer(t, echoPolicyDialer(t, netip.MustParseAddr("203.0.113.10")))
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:8443", Once: true})
	env.call(t, CtlInit, InitArgs{SessionID: "reload23", Agent: "shell", Policy: pol})
	st, br := openOpaquePathFlow(t, env, "explicit")
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial CONNECT = %v, %v", resp, err)
	}
	_ = resp.Body.Close()
	requireOpaqueEcho(t, st, br)
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
	requireOpaqueEcho(t, st, br)
	other, otherReader := openOpaquePathFlow(t, env, "explicit")
	defer other.Close()
	resp, err = http.ReadResponse(otherReader, nil)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an unrelated reload rearmed the live grant: %v, %v", resp, err)
	}
	_ = resp.Body.Close()
}

func TestOnceRevocationRechecksTheActualPrivateAddress(t *testing.T) {
	addr := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	env := startTestServer(t, echoPolicyDialer(t, addr))
	pol := onceTestPolicy(t, api.PresetOpen, api.Rule{Allow: true, Target: addr.String() + ":8443", Once: true})
	env.call(t, CtlInit, InitArgs{SessionID: "addr2345", Agent: "shell", Policy: pol})
	st, br := openOpaquePathFlow(t, env, "explicit")
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial CONNECT = %v, %v", resp, err)
	}
	_ = resp.Body.Close()
	requireOpaqueEcho(t, st, br)
	// The hostname remains open. Only its numeric backing grant is removed.
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetOpen}})
	requireFlowClosed(t, st, br)
}

func TestOncePostRegistrationRechecksPolicyAfterDial(t *testing.T) {
	ready, release := make(chan struct{}), make(chan struct{})
	peerEnded := make(chan []byte, 1)
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			c, peer := net.Pipe()
			t.Cleanup(func() { _ = c.Close(); _ = peer.Close() })
			go func() { defer peer.Close(); b, _ := io.ReadAll(peer); peerEnded <- b }()
			return c, nil
		},
	}
	env := startTestServer(t, d)
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:8443", Once: true})
	env.call(t, CtlInit, InitArgs{SessionID: "race2345", Agent: "shell", Policy: pol})
	st, _ := openOpaquePathFlow(t, env, "explicit")
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("the flow did not reach its blocked dial")
	}
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetLocked}})
	close(release)
	_ = st.SetDeadline(time.Now().Add(3 * time.Second))
	if reply, err := io.ReadAll(st); err != nil || len(reply) != 0 {
		t.Fatalf("a revoked admission sent a CONNECT success: %q, %v", reply, err)
	}
	select {
	case data := <-peerEnded:
		if len(data) != 0 {
			t.Fatalf("a revoked admission forwarded %q upstream", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the revoked upstream connection stayed open")
	}
}

func TestPendingTLSFlowIsRevokedBeforeClientHello(t *testing.T) {
	env := startTestServer(t, echoPolicyDialer(t, netip.MustParseAddr("203.0.113.10")))
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:443"})
	env.call(t, CtlInit, InitArgs{SessionID: "hello234", Agent: "shell", Policy: pol})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "example.com", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(st, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(st)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial CONNECT = %v, %v", resp, err)
	}
	_ = resp.Body.Close()
	// Deliberately send no ClientHello. Revocation must still find the flow.
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetLocked}})
	requireFlowClosed(t, st, br)
}
