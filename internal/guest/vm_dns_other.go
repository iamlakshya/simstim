//go:build !linux

package guest

import (
	"fmt"
	"net"
)

func verifyVMDNSWildcard(net.Addr) error {
	return fmt.Errorf("private microvm DNS requires Linux PID1")
}
