//go:build linux

package guest

import (
	"fmt"
	"net"
	"os"
)

// This private scope is used only by the guarded VM init when Docker=true.
// The ordinary/inherited DNS APIs remain loopback-only. The VMM has no NIC;
// wildcard binds serve its private Docker bridges without another DNS engine.
func verifyVMDNSWildcard(address net.Addr) error {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return vmError("private VM DNS requires actual root PID1")
	}
	return vmDNSWildcardAddress(address)
}
func vmDNSWildcardAddress(address net.Addr) error {
	var ip net.IP
	var port int
	switch value := address.(type) {
	case *net.UDPAddr:
		ip, port = value.IP, value.Port
	case *net.TCPAddr:
		ip, port = value.IP, value.Port
	default:
		return fmt.Errorf("private VM DNS has an invalid socket type")
	}
	if ip.To4() == nil || !ip.IsUnspecified() || port != 53 {
		return fmt.Errorf("private VM DNS requires exactly wildcard IPv4 port53")
	}
	return nil
}
