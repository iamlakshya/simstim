package egress

import (
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDNSWireRejectsOversizedWellFormedOPT(t *testing.T) {
	// The OPT resource's own 16-bit length fits. The header and question
	// make the complete packet too large for DNS-over-TCP framing.
	raw := buildEDNSQuery(t, "example.com.", dnsmessage.TypeA, ".", []dnsmessage.Option{{
		Code: 65000,
		Data: make([]byte, 65531),
	}})
	if len(raw) <= 65535 {
		t.Fatalf("fixture has %d bytes, want more than 65535", len(raw))
	}
	if _, err := parseDNSQuery(raw); err == nil {
		t.Fatal("accepted a structurally valid DNS packet beyond its framing limit")
	}
}
