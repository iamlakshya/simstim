package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestOnceAuditPrecedesTheExactEndEvent(t *testing.T) {
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:80", Once: true, GrantID: "11111111111111111111111111111111"})
	env, _, _ := startOnceHTTP(t, pol, nil, false)
	if status, err := onceHTTP(env, "example.com", "/first"); err != nil || status != http.StatusOK {
		t.Fatalf("first request = %d, %v", status, err)
	}
	waitLeaseEvent(t, env, false)
	env.mu.Lock()
	events := slices.Clone(env.events)
	ended := slices.Clone(env.ended)
	env.mu.Unlock()
	if len(events) != 2 || events[0] != EventAudit || events[1] != EventOnceEnd {
		t.Fatalf("event order = %v, want audit then once_end", events)
	}
	if len(ended) != 1 || ended[0].ID != pol.Rules[0].ID || ended[0].GrantID != pol.Rules[0].GrantID || !ended[0].Until.Equal(pol.Rules[0].Until) {
		t.Fatalf("ended leases = %+v, want exact %+v", ended, pol.Rules[0])
	}
}

func TestTimedGrantExpiresWithoutTrafficOrPolicyReload(t *testing.T) {
	var tick atomic.Int64
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tick.Store(start.UnixNano())
	clock := func() time.Time { return time.Unix(0, tick.Load()).UTC() }
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			c, peer := net.Pipe()
			t.Cleanup(func() { _ = c.Close(); _ = peer.Close() })
			go func() { defer peer.Close(); _, _ = io.Copy(peer, peer) }()
			return c, nil
		},
	}
	env := startTestServerWithClock(t, d, clock)
	rule := api.Rule{Allow: true, Target: "example.com:8443", Until: start.Add(time.Second), GrantID: "11111111111111111111111111111111"}
	pol := onceTestPolicy(t, api.PresetLocked, rule)
	env.call(t, CtlInit, InitArgs{SessionID: "lease234", Agent: "shell", Policy: pol})
	st, br := openOpaquePathFlow(t, env, "explicit")
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial CONNECT = %v, %v", resp, err)
	}
	_ = resp.Body.Close()
	if _, err := io.WriteString(st, "ping\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := br.ReadString('\n'); err != nil || line != "ping\n" {
		t.Fatalf("initial flow = %q, %v", line, err)
	}
	tick.Store(start.Add(2 * time.Second).UnixNano())
	env.srv.wakeExpiry()
	_ = st.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = br.ReadByte()
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("expired idle flow did not close: %v", err)
	}
	waitLeaseEvent(t, env, true)
	env.mu.Lock()
	expired := slices.Clone(env.expiries)
	env.mu.Unlock()
	if len(expired) != 1 || expired[0].ID != pol.Rules[0].ID || expired[0].GrantID != rule.GrantID || !expired[0].Until.Equal(rule.Until) {
		t.Fatalf("expiry event = %+v, want exact lease", expired)
	}
}

func TestIdleLeaseExpiryIsNotReEmittedByUnchangedReload(t *testing.T) {
	var tick atomic.Int64
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tick.Store(start.UnixNano())
	clock := func() time.Time { return time.Unix(0, tick.Load()).UTC() }
	env := startTestServerWithClock(t, &Dialer{}, clock)
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:80", Until: start.Add(time.Second)})
	env.call(t, CtlInit, InitArgs{SessionID: "idle2345", Agent: "shell", Policy: pol})
	tick.Store(start.Add(2 * time.Second).UnixNano())
	env.srv.wakeExpiry()
	waitLeaseEvent(t, env, true)
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
	env.srv.expire(env.srv.currentEngine(), clock())
	// The response follows every event written by the explicit expire call.
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
	env.mu.Lock()
	count := len(env.expiries)
	env.mu.Unlock()
	if count != 1 {
		t.Fatalf("an unchanged policy re-emitted expiry %d times", count)
	}
}

func TestLeaseKeyKeepsFullTimeIdentity(t *testing.T) {
	first := time.Unix(0, 0).UTC()
	far := time.Unix(18446744073, 709551616).UTC()
	a := (compiledRule{id: "gabcdef", until: first}).instanceKey()
	b := (compiledRule{id: "gabcdef", until: far}).instanceKey()
	if a == b {
		t.Fatal("distinct lease instants shared an identity")
	}
	zone := time.FixedZone("elsewhere", 3600)
	if canonicalInstant(first.In(zone)) != canonicalInstant(first) || canonicalInstant(time.Time{}.In(zone)) != (time.Time{}) {
		t.Fatal("equal instants did not have canonical identity")
	}
}

func waitLeaseEvent(t *testing.T, env *testEnv, expiry bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		env.mu.Lock()
		ready := len(env.ended) > 0
		if expiry {
			ready = len(env.expiries) > 0
		}
		env.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the exact lease end event did not arrive")
}
