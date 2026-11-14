package egress

import (
	"context"
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func onceAddrEngine(t *testing.T) *Engine {
	t.Helper()
	pol := pathTestPolicy(t, api.PresetLocked, "127.0.0.1:80")
	pol.Rules[0].Once = true
	eng, err := NewEngine("shell", pol)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestCheckAddrSpendsMappedOnceGrantOnlyOnce(t *testing.T) {
	eng := onceAddrEngine(t)
	d := &Dialer{}
	mapped := netip.MustParseAddr("::ffff:127.0.0.1")
	if rule, ok := d.CheckAddr(mapped, 80, eng.AllowsAddr); !ok {
		t.Fatalf("first mapped address check failed: %s", rule)
	}
	if _, ok := d.CheckAddr(mapped, 80, eng.AllowsAddr); ok {
		t.Fatal("an independent check reused a spent once grant")
	}
	if eng.AllowsAddr(mapped.Unmap(), 80) {
		t.Fatal("the successful mapped address check did not spend its grant")
	}
}

func TestResolveAndCheckSpendsDuplicateOnceGrantOnlyOnce(t *testing.T) {
	for _, addrs := range [][]netip.Addr{
		{netip.MustParseAddr("::ffff:127.0.0.1")},
		{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::ffff:127.0.0.1"), netip.MustParseAddr("127.0.0.1")},
	} {
		eng := onceAddrEngine(t)
		d := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) { return addrs, nil }}
		got, rule, err := d.ResolveAndCheck(context.Background(), "allowed.example.com", 80, eng.AllowsAddr)
		if err != nil || len(got) != len(addrs) {
			t.Fatalf("first address check = %v, %q, %v", got, rule, err)
		}
		if _, _, err := d.ResolveAndCheck(context.Background(), "allowed.example.com", 80, eng.AllowsAddr); err == nil {
			t.Fatal("an independent resolution reused a spent once grant")
		}
	}
}
