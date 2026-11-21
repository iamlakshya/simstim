package client

import (
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/nettarget"
)

// A held export names one socket target, never a CIDR allowance. Reuse the
// actual runtime's static address restrictions, including transition addresses.
// The remote actor still checks its own interfaces and every actual dial;
// this check cannot supply a private-address exception or a runtime grant.
func heldPublicLiteralV1(target nettarget.Target) bool {
	if !target.Prefix.IsValid() || target.Prefix.Bits() != target.Prefix.Addr().BitLen() || target.Port < 1 || target.Port > 65535 || target.Wildcard || target.Path != "" {
		return false
	}
	_, allowed := new(egress.Dialer).CheckAddr(target.Prefix.Addr(), target.Port, nil)
	return allowed
}
