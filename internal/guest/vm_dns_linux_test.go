//go:build linux

package guest

import (
	"net"
	"os"
	"testing"
)

func TestVMPrivateWildcardDNSDoesNotWidenOrdinaryLoopbackContract(t *testing.T) {
	address := &net.UDPAddr{IP: net.IPv4zero, Port: 53}
	if err := vmDNSWildcardAddress(address); err != nil {
		t.Fatal(err)
	}
	if verifyScopedDNSAddr(address, dnsLoopbackScope) == nil {
		t.Fatal("ordinary/inherited DNS accepted wildcard")
	}
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		if verifyScopedDNSAddr(address, dnsPrivateVMScope) == nil {
			t.Fatal("an ordinary process claimed VM-private DNS authority")
		}
	}
	for _, address := range []net.Addr{&net.UDPAddr{IP: net.IPv4zero, Port: 5353}, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53}, &net.TCPAddr{IP: net.ParseIP("203.0.113.1"), Port: 53}, &net.TCPAddr{IP: net.IPv6zero, Port: 53}} {
		if vmDNSWildcardAddress(address) == nil {
			t.Fatal("private VM scope accepted another address or port", address)
		}
	}
}
