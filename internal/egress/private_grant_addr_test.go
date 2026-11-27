package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestExistingNumericExceptionsDecideBeforeUnknownWithoutAHold(t *testing.T) {
	private := netip.AddrFrom4([4]byte{10, 23, 0, 1})
	for _, target := range []string{private.String(), netip.PrefixFrom(private, 32).String()} {
		server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk, Rules: []api.Rule{{Layer: api.LayerSession, Target: target, Allow: true}}}})
		request := Request{Host: private.String(), Port: 443}
		decision := server.awaitUnknown(t.Context(), request, request.Host, "explicit", nil, func(engine *Engine, request Request) Decision { return engine.evaluate(request, nil, evalPrecheck) })
		if !decision.Allow || len(server.pendingNetworkSnapshot().Requests) != 0 || !server.currentEngine().AllowsAddr(private, 443) {
			t.Fatal("an existing exact/CIDR numeric exception fabricated a held question or changed legacy meaning")
		}
	}
}

func TestSensitiveOwnDNSUsesOnlyIndependentExistingNumericException(t *testing.T) {
	private := netip.AddrFrom4([4]byte{10, 23, 0, 1})
	for _, publicOnly := range []bool{false, true} {
		server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk, Rules: []api.Rule{{Layer: api.LayerAll, Target: private.String(), Allow: true}}}, Secrets: map[string]string{"EXPOSED": "privatevalue"}})
		request := Request{Host: "privatevalue.example.test", Port: 443}
		entry, _ := privateQuestion(t, server, request.Host, 443, 0)
		server.policyMu.Lock()
		_, err := server.installMemoryGrantLocked(t.Context(), entry, api.NetworkHoldSession, publicOnly, "live", request)
		server.policyMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		engine := server.currentEngine()
		permit := engine.NewPermit()
		defer permit.Close()
		if !engine.DecideWithPermit(request, permit).Allow {
			t.Fatal("actual sensitive session grant failed its canonical matcher")
		}
		dials := 0
		dialer := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{private}, nil }, DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			dials++
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		}}
		conn, _, _, err := dialer.dialWithPermit(t.Context(), func() *Engine { return server.currentEngine() }, request.Host, 443, permit, &request)
		if conn != nil {
			_ = conn.Close()
		}
		if publicOnly {
			if !errors.Is(err, ErrRefusedAddr) || dials != 0 {
				t.Fatal("public-only sensitive answer used a prior Own private exception")
			}
		} else if err != nil || dials != 1 {
			t.Fatal("Own DNS answer lost its independent existing numeric exception", err)
		}
		if !engine.AllowsAddr(private, 443) {
			t.Fatal("legacy numeric exception API changed")
		}
	}
}
