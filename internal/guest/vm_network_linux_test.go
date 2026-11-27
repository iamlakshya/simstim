//go:build linux

package guest

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
)

func TestVMOriginalDestinationPreservesIPv4IPv6AndPort(t *testing.T) {
	for _, address := range []string{"203.0.113.9:443", "[2001:db8::19]:8443"} {
		want := netip.MustParseAddrPort(address)
		data := make([]byte, 28)
		binary.BigEndian.PutUint16(data[2:], want.Port())
		if want.Addr().Is4() {
			binary.NativeEndian.PutUint16(data, unix.AF_INET)
			copy(data[4:], want.Addr().AsSlice())
		} else {
			binary.NativeEndian.PutUint16(data, unix.AF_INET6)
			copy(data[8:], want.Addr().AsSlice())
		}
		got, err := vmDecodeOriginalDestination(data)
		if err != nil || got != want {
			t.Fatal("redirect lost original endpoint", got, err)
		}
	}
	for _, data := range [][]byte{nil, {0, 0, 0, 0}, {unix.AF_INET, 0, 0, 0, 1, 2, 3, 4}, {unix.AF_UNIX, 0, 0, 80, 1, 2, 3, 4}} {
		if _, err := vmDecodeOriginalDestination(data); err == nil {
			t.Fatal("invalid original address accepted")
		}
	}
}

func TestVMSyntheticDNSMappingIsBoundedExpiringRoutingHint(t *testing.T) {
	m := newVMSyntheticMap()
	clock := time.Unix(100, 0)
	m.now = func() time.Time { return clock }
	name := dnsmessage.MustNewName("api.example.")
	question := dnsmessage.Question{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}
	query, _ := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 7}, Questions: []dnsmessage.Question{question}}).Pack()
	response, _ := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 7, Response: true}, Questions: []dnsmessage.Question{question}, Answers: []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 600}, Body: &dnsmessage.AResource{A: [4]byte{198, 18, 0, 7}}}}}).Pack()
	if err := m.observe(query, response); err != nil {
		t.Fatal(err)
	}
	synthetic := netip.MustParseAddr("198.18.0.7")
	if host, err := m.host(synthetic); err != nil || host != "api.example" {
		t.Fatal("synthetic name lost", host, err)
	}
	if host, err := m.host(netip.MustParseAddr("203.0.113.9")); err != nil || host != "203.0.113.9" {
		t.Fatal("real address rewritten", host, err)
	}
	clock = clock.Add(301 * time.Second)
	if _, err := m.host(synthetic); err == nil {
		t.Fatal("expired hint became continuing network authority")
	}
	response[0] ^= 1
	if m.observe(query, response) == nil {
		t.Fatal("mismatched DNS answer admitted")
	}
}

func TestVMNFTAtomicOutputRedirectLeavesGuestLocalServicesAlone(t *testing.T) {
	messages := vmNFTRedirectMessages(false)
	if len(messages) != 7 || messages[0].kind != unix.NFNL_MSG_BATCH_BEGIN || messages[6].kind != unix.NFNL_MSG_BATCH_END {
		t.Fatal("redirect is not one atomic transaction")
	}
	chain, err := vmNLAttrs(messages[2].data[4:])
	if err != nil {
		t.Fatal(err)
	}
	hook, err := vmNLAttrs(chain[4])
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(hook[1]) != 3 || int32(binary.BigEndian.Uint32(hook[2])) != -100 {
		t.Fatal("rule is not guest local-output NAT")
	}
	rule, err := vmNLAttrs(messages[3].data[4:])
	if err != nil {
		t.Fatal(err)
	}
	// The exact oifname match limits interception to dummy0. Loopback DNS,
	// proxy and guest-only services must not recurse through this redirect.
	if !containsVMBytes(rule[4], []byte("dummy0\x00")) {
		t.Fatal("external-route-only selector absent")
	}
	if messages[1].flags&unix.NLM_F_EXCL == 0 {
		t.Fatal("unexpected existing table could be mutated")
	}
}
func containsVMBytes(data, find []byte) bool {
	for i := 0; i+len(find) <= len(data); i++ {
		same := true
		for j := range find {
			if data[i+j] != find[j] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

func testVMNFTExpressions(t *testing.T, rule vmNLMessage) []map[uint16][]byte {
	t.Helper()
	attrs, err := vmNLAttrs(rule.data[4:])
	if err != nil {
		t.Fatal(err)
	}
	data := attrs[4]
	var expressions []map[uint16][]byte
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatal("short expression list")
		}
		size := int(binary.NativeEndian.Uint16(data))
		if size < 4 || size > len(data) {
			t.Fatal("invalid expression record")
		}
		expression, err := vmNLAttrs(data[4:size])
		if err != nil {
			t.Fatal(err)
		}
		expressions = append(expressions, expression)
		data = data[(size+3)&^3:]
	}
	return expressions
}

func TestVMDockerPreroutingKeepsLocalServicesAndRedirectsExternalTCP(t *testing.T) {
	messages := vmNFTRedirectMessages(true)
	if len(messages) != 9 {
		t.Fatal("Docker did not add its actual prerouting chain/rule")
	}
	chain, err := vmNLAttrs(messages[6].data[4:])
	if err != nil {
		t.Fatal(err)
	}
	hook, err := vmNLAttrs(chain[4])
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(hook[1]) != 0 || int32(binary.BigEndian.Uint32(hook[2])) != -100 {
		t.Fatal("container packets skip required prerouting NAT")
	}
	expressions := testVMNFTExpressions(t, messages[7])
	names := []string{"meta", "cmp", "meta", "cmp", "meta", "cmp", "payload", "bitwise", "cmp", "immediate", "redir"}
	if len(expressions) != len(names) {
		t.Fatal("container transport predicate incomplete")
	}
	attributes := make([]map[uint16][]byte, len(names))
	for i, expression := range expressions {
		if string(expression[1]) != names[i]+"\x00" {
			t.Fatal("unexpected expression ordering", i, string(expression[1]))
		}
		attributes[i], err = vmNLAttrs(expression[2])
		if err != nil {
			t.Fatal(err)
		}
	}
	if binary.BigEndian.Uint32(attributes[0][2]) != 6 || binary.BigEndian.Uint32(attributes[1][2]) != 1 || binary.BigEndian.Uint32(attributes[2][2]) != 15 {
		t.Fatal("loopback exclusion/nfproto selector changed")
	}
	if binary.BigEndian.Uint32(attributes[6][2]) != 1 || binary.BigEndian.Uint32(attributes[6][3]) != 16 || binary.BigEndian.Uint32(attributes[6][4]) != 4 {
		t.Fatal("destination payload is not the IPv4 network-header address")
	}
	mask, err := vmNLAttrs(attributes[7][4])
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := vmNLAttrs(attributes[8][3])
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(attributes[8][2]) != 1 || len(mask[1]) != 4 || len(comparison[1]) != 4 {
		t.Fatal("bridge local bypass is not a closed prefix inequality")
	}
	for _, sample := range []struct {
		address  [4]byte
		redirect bool
	}{{[4]byte{172, 30, 0, 1}, false}, {[4]byte{172, 30, 19, 7}, false}, {[4]byte{198, 18, 0, 3}, true}, {[4]byte{203, 0, 113, 9}, true}} {
		equal := true
		for i, value := range sample.address {
			if value&mask[1][i] != comparison[1][i] {
				equal = false
			}
		}
		if (!equal) != sample.redirect {
			t.Fatal("container DNS/local services or outside TCP routed incorrectly", sample)
		}
	}
}

func TestVMOutputDropsNonTCPWithCompiledVerdictAndNoUIDBypass(t *testing.T) {
	messages := vmNFTRedirectMessages(false)
	expressions := testVMNFTExpressions(t, messages[5])
	if len(expressions) != 5 {
		t.Fatal("nonTCP output filter missing")
	}
	for _, expression := range expressions {
		if string(expression[1]) == "reject\x00" {
			t.Fatal("rule requires disabled NFT_REJECT")
		}
		if string(expression[1]) == "meta\x00" {
			attrs, err := vmNLAttrs(expression[2])
			if err != nil {
				t.Fatal(err)
			}
			if binary.BigEndian.Uint32(attrs[2]) == 10 {
				t.Fatal("root sourceUID bypasses canonical egress")
			}
		}
	}
	last, err := vmNLAttrs(expressions[4][2])
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(last[1]) != 0 {
		t.Fatal("drop is not a verdict register write")
	}
	data, err := vmNLAttrs(last[2])
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := vmNLAttrs(data[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(verdict[1]) != 4 || binary.BigEndian.Uint32(verdict[1]) != 0 {
		t.Fatal("nonTCP path is not actual NF_DROP")
	}
}
