package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

func TestOnceGrantCarriesItsFirstOpaqueConnection(t *testing.T) {
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
					go func() { defer peer.Close(); _, _ = io.Copy(peer, peer) }()
					return c, nil
				},
			}
			env := startTestServer(t, d)
			env.call(t, CtlInit, InitArgs{
				SessionID: "once2345", Agent: "shell",
				Policy: onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:8443", Once: true}),
			})
			st, br := openOpaquePathFlow(t, env, via)
			if via == "explicit" {
				resp, err := http.ReadResponse(br, nil)
				if err != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("first CONNECT = %v, %v", resp, err)
				}
				_ = resp.Body.Close()
			}
			if _, err := io.WriteString(st, "ping\n"); err != nil {
				t.Fatalf("the first granted flow cannot send bytes: %v", err)
			}
			if echo, err := br.ReadString('\n'); err != nil || echo != "ping\n" {
				t.Fatalf("the first granted flow cannot carry bytes: %q, %v", echo, err)
			}
			_ = st.Close()
			if dials.Load() != 1 {
				t.Fatalf("upstream dials = %d, want 1", dials.Load())
			}
		})
	}
}

func TestOnceNumericGrantAllowsItsFirstDirectFlow(t *testing.T) {
	addr := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	pol := onceTestPolicy(t, api.PresetOpen, api.Rule{Allow: true, Target: addr.String() + ":80", Once: true})
	env, dials, lookups := startOnceHTTP(t, pol, []netip.Addr{addr}, false)
	if status, err := onceHTTP(env, addr.String(), "/first"); err != nil || status != http.StatusOK {
		t.Fatalf("first direct numeric grant = %d, %v, want 200", status, err)
	}
	if status, err := onceHTTP(env, addr.String(), "/second"); err != nil || status != http.StatusForbidden {
		t.Fatalf("second direct numeric grant = %d, %v, want 403", status, err)
	}
	if dials.Load() != 1 || lookups.Load() != 0 {
		t.Fatalf("numeric flow made %d dials and %d DNS lookups", dials.Load(), lookups.Load())
	}
}

func TestOnceNumericGrantCannotBeReusedThroughHostnames(t *testing.T) {
	addr := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	pol := onceTestPolicy(t, api.PresetOpen, api.Rule{Allow: true, Target: addr.String() + ":80", Once: true})
	env, dials, _ := startOnceHTTP(t, pol, []netip.Addr{addr}, false)
	if status, err := onceHTTP(env, "first.example.com", "/first"); err != nil || status != http.StatusOK {
		t.Fatalf("first hostname flow = %d, %v, want 200", status, err)
	}
	if status, err := onceHTTP(env, "second.example.com", "/second"); err != nil || status != http.StatusForbidden {
		t.Fatalf("a second hostname reused the numeric once grant: %d, %v", status, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("one numeric grant authorised %d upstream dials", dials.Load())
	}
}

func TestOnceCIDRGrantChecksAddressesAndRetriesWithinOneFlow(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			addresses := []netip.Addr{
				netip.AddrFrom4([4]byte{127, 0, 0, 1}), netip.AddrFrom4([4]byte{127, 0, 0, 2}),
			}
			pol := onceTestPolicy(t, api.PresetOpen, api.Rule{Allow: true, Target: "127.0.0.0/8:80", Once: true})
			env, _, _ := startOnceHTTP(t, pol, addresses, retry)
			if status, err := onceHTTP(env, "first.example.com", "/first"); err != nil || status != http.StatusOK {
				t.Fatalf("one flow checking two addresses and retry=%v returned %d, %v", retry, status, err)
			}
			if status, err := onceHTTP(env, "second.example.com", "/second"); err != nil || status != http.StatusForbidden {
				t.Fatalf("a later flow reused the CIDR grant: %d, %v", status, err)
			}
		})
	}
}

func TestOnceHostAndNumericGrantsAreChargedTogether(t *testing.T) {
	addr := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	pol := onceTestPolicy(t, api.PresetLocked,
		api.Rule{Allow: true, Target: "first.example.com:80", Once: true},
		api.Rule{Allow: true, Target: "second.example.com:80"},
		api.Rule{Allow: true, Target: addr.String() + ":80", Once: true},
	)
	env, _, _ := startOnceHTTP(t, pol, []netip.Addr{addr}, false)
	if status, err := onceHTTP(env, "first.example.com", "/first"); err != nil || status != http.StatusOK {
		t.Fatalf("flow requiring two once grants = %d, %v", status, err)
	}
	if status, err := onceHTTP(env, "second.example.com", "/second"); err != nil || status != http.StatusForbidden {
		t.Fatalf("the numeric grant was not charged with its host grant: %d, %v", status, err)
	}
}

func TestOnceBudgetSurvivesAnUnrelatedPolicyReload(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:80", Once: true})
	env, dials, _ := startOnceHTTP(t, pol, nil, false)
	if status, err := onceHTTP(env, "example.com", "/first"); err != nil || status != http.StatusOK {
		t.Fatalf("first request = %d, %v", status, err)
	}
	// The audit is sent after ServeFlow returns and removes the finished flow.
	env.waitAudits(t, 1)
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
	if status, err := onceHTTP(env, "example.com", "/second"); err != nil || status != http.StatusForbidden {
		t.Fatalf("an unchanged reload rearmed the spent grant: %d, %v", status, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("a spent grant authorised %d dials after reload", dials.Load())
	}
}

func TestOnceGrantAdmitsOnlyOneConcurrentFlow(t *testing.T) {
	for _, numeric := range []bool{false, true} {
		t.Run(fmt.Sprint(numeric), func(t *testing.T) {
			preset, target := api.PresetLocked, "example.com:80"
			var addresses []netip.Addr
			if numeric {
				preset = api.PresetOpen
				addresses = []netip.Addr{netip.AddrFrom4([4]byte{127, 0, 0, 1})}
				target = addresses[0].String() + ":80"
			}
			pol := onceTestPolicy(t, preset, api.Rule{Allow: true, Target: target, Once: true})
			env, dials, _ := startOnceHTTP(t, pol, addresses, false)
			type answer struct {
				status int
				err    error
			}
			answers := make(chan answer, 8)
			start := make(chan struct{})
			for range 8 {
				go func() {
					<-start
					status, err := onceHTTP(env, "example.com", "/race")
					answers <- answer{status: status, err: err}
				}()
			}
			close(start)
			allowed := 0
			for range 8 {
				got := <-answers
				switch {
				case got.err != nil:
					t.Errorf("concurrent request failed: %v", got.err)
				case got.status == http.StatusOK:
					allowed++
				case got.status != http.StatusForbidden:
					t.Errorf("concurrent request returned %d", got.status)
				}
			}
			if allowed != 1 || dials.Load() != 1 {
				t.Fatalf("one grant allowed %d requests and %d upstream dials", allowed, dials.Load())
			}
		})
	}
}

func onceTestPolicy(t *testing.T, preset api.Preset, rules ...api.Rule) api.Policy {
	t.Helper()
	pol, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Preset: preset, Rules: rules}})
	if err != nil {
		t.Fatal(err)
	}
	pol.Unknown = ""
	pol.Network.Unknown = nil
	return pol
}

func startOnceHTTP(t *testing.T, pol api.Policy, addresses []netip.Addr, failFirst bool) (*testEnv, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	t.Cleanup(ts.Close)
	dials, lookups := &atomic.Int32{}, &atomic.Int32{}
	if len(addresses) == 0 {
		addresses = []netip.Addr{netip.MustParseAddr("203.0.113.10")}
	}
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			lookups.Add(1)
			return addresses, nil
		},
		DialTCP: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
			if dials.Add(1) == 1 && failFirst {
				return nil, errors.New("test dial failed")
			}
			var nd net.Dialer
			return nd.DialContext(ctx, "tcp", ts.Listener.Addr().String())
		},
	}
	env := startTestServer(t, d)
	env.call(t, CtlInit, InitArgs{SessionID: "once2345", Agent: "shell", Policy: pol})
	return env, dials, lookups
}

func onceHTTP(env *testEnv, host, path string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: host, Port: 80})
	if err != nil {
		return 0, err
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(5 * time.Second))
	authority := net.JoinHostPort(host, "80")
	if _, err := fmt.Fprintf(st, "GET http://%s%s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, path, authority); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(st), nil)
	if err != nil {
		return 0, err
	}
	_, err = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, err
}
