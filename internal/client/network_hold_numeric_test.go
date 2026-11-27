package client

import (
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/nettarget"
)

func TestHeldPublicLiteralKeepsActualRuntimeAddressRestrictions(t *testing.T) {
	for _, raw := range []string{"8.8.8.8:443", "[2606:4700:4700::1111]:443"} {
		target, err := nettarget.Parse(raw)
		if err != nil || !heldPublicLiteralV1(target) {
			t.Fatal("an exact public literal lost its narrow held export", raw, err)
		}
	}
	for _, raw := range []string{
		"8.8.8.0/24:443", "8.8.8.8", "127.0.0.1:443", netip.AddrFrom4([4]byte{10, 0, 0, 1}).String() + ":443", netip.AddrFrom4([4]byte{100, 64, 0, 1}).String() + ":443", "169.254.169.254:443",
		"[::1]:443", "[::ffff:127.0.0.1]:443", "[64:ff9b::a00:1]:443", "[2002:0a00:0001::1]:443", "[ff02::1]:443", "api.example:443",
	} {
		target, err := nettarget.Parse(raw)
		if err != nil {
			t.Fatal("fixture target syntax changed", raw, err)
		}
		if heldPublicLiteralV1(target) {
			t.Fatal("a private, transition, wider or non-literal target became a public held export", raw)
		}
	}
}
