package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestOnceFailedDialReleasesTheHostReservation(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:80", Once: true})
	env, _, _ := startOnceHTTP(t, pol, nil, true)
	if status, err := onceHTTP(env, "example.com", "/failed"); err != nil || status != http.StatusBadGateway {
		t.Fatalf("failed TCP attempt = %d, %v", status, err)
	}
	if status, err := onceHTTP(env, "example.com", "/valid"); err != nil || status != http.StatusOK {
		t.Fatalf("failed TCP attempt spent the grant: %d, %v", status, err)
	}
	if status, err := onceHTTP(env, "example.com", "/spent"); err != nil || status != http.StatusForbidden {
		t.Fatalf("successful TCP attempt did not spend the grant: %d, %v", status, err)
	}
}

func TestOnceFailedDNSReleasesTheHostReservation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	t.Cleanup(ts.Close)
	var lookups atomic.Int32
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			if lookups.Add(1) == 1 {
				return nil, errors.New("test DNS failure")
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
			var nd net.Dialer
			return nd.DialContext(ctx, "tcp", ts.Listener.Addr().String())
		},
	}
	env := startTestServer(t, d)
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:80", Once: true})
	env.call(t, CtlInit, InitArgs{SessionID: "failed23", Agent: "shell", Policy: pol})
	if status, err := onceHTTP(env, "example.com", "/failed"); err != nil || status != http.StatusBadGateway {
		t.Fatalf("failed DNS attempt = %d, %v", status, err)
	}
	if status, err := onceHTTP(env, "example.com", "/valid"); err != nil || status != http.StatusOK {
		t.Fatalf("failed DNS attempt spent the grant: %d, %v", status, err)
	}
}

func TestOnceFailedCandidateDoesNotReleaseTheHostClaim(t *testing.T) {
	addr := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	pol := onceTestPolicy(t, api.PresetLocked,
		api.Rule{Allow: true, Target: "example.com:80", Once: true},
		api.Rule{Allow: true, Target: addr.String() + ":80", Once: true},
	)
	e := sharedPolicyEngine(t, nil, pol)
	p := e.NewPermit()
	defer p.Close()
	req := Request{Host: "example.com", Port: 80}
	if !e.DecideWithPermit(req, p).Allow {
		t.Fatal("host claim was denied")
	}
	before := p.checkpoint()
	if !e.ReserveAddr(addr, 80, p) {
		t.Fatal("numeric claim was denied")
	}
	p.rollback(before)
	if !e.Revalidate(req, p).Allow {
		t.Fatal("candidate rollback released the host claim")
	}
	other := e.NewPermit()
	defer other.Close()
	if e.DecideWithPermit(req, other).Allow {
		t.Fatal("another flow stole the host reservation")
	}
	if !e.ReserveAddr(addr, 80, other) {
		t.Fatal("candidate rollback failed to release its numeric reservation")
	}
}

func TestOnceFailedCandidateDoesNotSpendAnotherAddressGrant(t *testing.T) {
	first := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	second := netip.AddrFrom4([4]byte{127, 0, 0, 2})
	pol := onceTestPolicy(t, api.PresetOpen,
		api.Rule{Allow: true, Target: first.String() + ":80", Once: true},
		api.Rule{Allow: true, Target: second.String() + ":80", Once: true},
	)
	e := sharedPolicyEngine(t, nil, pol)
	var failed atomic.Bool
	d := &Dialer{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host == "first.example.com" {
				return []netip.Addr{first, second}, nil
			}
			return []netip.Addr{first}, nil
		},
		DialTCP: func(_ context.Context, ap netip.AddrPort) (net.Conn, error) {
			if ap.Addr() == first && failed.CompareAndSwap(false, true) {
				return nil, errors.New("first candidate failed")
			}
			c, peer := net.Pipe()
			_ = peer.Close()
			return c, nil
		},
	}
	p := e.NewPermit()
	defer p.Close()
	if !e.DecideWithPermit(Request{Host: "first.example.com", Port: 80}, p).Allow {
		t.Fatal("initial hostname was denied")
	}
	conn, selected, _, err := d.DialWithPermit(context.Background(), e, "first.example.com", 80, p)
	if err != nil || selected != second {
		t.Fatalf("fallback dial selected %v, %v, want the second candidate", selected, err)
	}
	p.Commit()
	_ = conn.Close()
	p.Close()
	other := e.NewPermit()
	defer other.Close()
	if !e.DecideWithPermit(Request{Host: "next.example.com", Port: 80}, other).Allow {
		t.Fatal("later hostname was denied")
	}
	conn, selected, _, err = d.DialWithPermit(context.Background(), e, "next.example.com", 80, other)
	if err != nil || selected != first {
		t.Fatalf("an unused candidate grant was spent: selected=%v error=%v", selected, err)
	}
	other.Commit()
	_ = conn.Close()
}
