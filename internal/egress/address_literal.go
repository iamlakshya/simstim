package egress

import "net/netip"

// isNumericAddr classifies an already canonical host for protocol checks.
// Destination grammar and rule validation stay in the shared nettarget parser.
func isNumericAddr(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}
