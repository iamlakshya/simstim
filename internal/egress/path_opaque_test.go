package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

func TestOpaqueFlowsDoNotBypassPathDenies(t *testing.T) {
	for _, tc := range []struct {
		name, host, agent string
		preset            api.Preset
		layers            []api.Layer
		opaqueAllowed     bool
	}{
		{"open lock", "example.com", "shell", api.PresetOpen, lockedPathDeny("example.com"), false},
		{"balanced lock", "api.github.com", "shell", api.PresetBalanced, lockedPathDeny("api.github.com"), false},
		{"agent lock", "api.anthropic.com", "claude", api.PresetLocked, lockedPathDeny("api.anthropic.com"), false},
		{"host grant below lock", "example.com", "shell", api.PresetLocked, append(lockedPathDeny("example.com"),
			api.Layer{Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Target: "example.com"}}}), false},
		{"same layer deny", "example.com", "shell", api.PresetLocked, []api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{
			{Allow: false, Target: "example.com/admin/"}, {Allow: true, Target: "example.com"},
		}}}, false},
		{"narrower deny", "example.com", "shell", api.PresetLocked, []api.Layer{
			{Kind: api.LayerHost, Rules: []api.Rule{{Allow: true, Target: "example.com"}}},
			{Kind: api.LayerSession, Rules: []api.Rule{{Allow: false, Target: "example.com/admin/"}}},
		}, false},
		{"narrower unlocked override", "example.com", "shell", api.PresetLocked, []api.Layer{
			{Kind: api.LayerHost, Rules: []api.Rule{{Allow: false, Target: "example.com/admin/"}}},
			{Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Target: "example.com"}}},
		}, true},
		{"wildcard child lock", "sub.example.com", "shell", api.PresetOpen, lockedPathDeny("*.example.com"), false},
		{"wildcard apex outside lock", "example.com", "shell", api.PresetOpen, lockedPathDeny("*.example.com"), true},
		{"other port outside lock", "example.com", "shell", api.PresetOpen, lockedPathDeny("example.com:8443"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layers := append([]api.Layer{{Kind: api.LayerAll, Preset: tc.preset}}, tc.layers...)
			pol, _, err := MergeLayers(layers)
			if err != nil {
				t.Fatal(err)
			}
			eng, err := NewEngine(tc.agent, pol)
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"CONNECT", "TCP"} {
				got := eng.Decide(Request{Host: tc.host, Port: 443, Method: method})
				if got.Allow != tc.opaqueAllowed {
					t.Errorf("%s with no visible path = %+v, want allow=%v", method, got, tc.opaqueAllowed)
				}
			}
			if got := eng.Decide(Request{Host: tc.host, Port: 443, Method: "GET", Path: "/allowed"}); !got.Allow {
				t.Errorf("a path deny closed an allowed visible sibling: %+v", got)
			}
		})
	}
}

func lockedPathDeny(host string) []api.Layer {
	return []api.Layer{{Kind: api.LayerHost, Lock: &api.Lock{Deny: []string{host + "/admin/"}}}}
}

func TestOpaqueFlowRevokedByAPathDeny(t *testing.T) {
	for _, via := range []string{"explicit", "transparent"} {
		t.Run(via, func(t *testing.T) {
			var dials atomic.Int32
			d := &Dialer{
				Lookup: func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
				},
				DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
					dials.Add(1)
					c, peer := net.Pipe()
					t.Cleanup(func() { _ = c.Close(); _ = peer.Close() })
					go func() {
						defer peer.Close()
						_, _ = io.Copy(peer, peer)
					}()
					return c, nil
				},
			}
			env := startTestServer(t, d)
			env.call(t, CtlInit, InitArgs{
				SessionID: "opaque23", SessionRef: "path-box", Agent: "shell",
				Policy: pathTestPolicy(t, api.PresetLocked, "example.com:8443"),
			})
			st, br := openOpaquePathFlow(t, env, via)
			if via == "explicit" {
				resp, err := http.ReadResponse(br, nil)
				if err != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("initial CONNECT = %v, %v", resp, err)
				}
				_ = resp.Body.Close()
			}
			if _, err := io.WriteString(st, "ping\n"); err != nil {
				t.Fatal(err)
			}
			if echo, err := br.ReadString('\n'); err != nil || echo != "ping\n" {
				t.Fatalf("initial opaque echo = %q, %v", echo, err)
			}
			pol, _, err := mergePathLock("example.com:8443/admin/", "example.com:8443")
			if err != nil {
				t.Fatal(err)
			}
			env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
			_ = st.SetReadDeadline(time.Now().Add(2 * time.Second))
			var timeout net.Error
			if _, err := br.ReadByte(); err == nil {
				t.Fatal("the path deny left the existing opaque flow open")
			} else if errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatal("the path deny did not revoke the existing flow before its read deadline")
			}
			_ = st.Close()

			next, nextReader := openOpaquePathFlow(t, env, via)
			if via == "explicit" {
				resp, err := http.ReadResponse(nextReader, nil)
				if err != nil || resp.StatusCode != http.StatusForbidden {
					t.Fatalf("CONNECT after a path deny = %v, %v", resp, err)
				}
				_ = resp.Body.Close()
			} else if _, err := nextReader.ReadByte(); err != io.EOF {
				t.Fatalf("transparent flow after a path deny returned %v, want EOF", err)
			}
			_ = next.Close()
			if dials.Load() != 1 {
				t.Fatalf("upstream dials = %d, want only the initial allowed flow", dials.Load())
			}
		})
	}
}

func openOpaquePathFlow(t *testing.T, env *testEnv, via string) (*link.Stream, *bufio.Reader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: via, Host: "example.com", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_ = st.SetDeadline(time.Now().Add(5 * time.Second))
	if via == "explicit" {
		if _, err := io.WriteString(st, "CONNECT example.com:8443 HTTP/1.1\r\nHost: example.com:8443\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
	}
	return st, bufio.NewReader(st)
}
