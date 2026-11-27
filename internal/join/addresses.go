package join

import (
	"net"

	"github.com/iamlakshya/simstim/internal/join/code"
)

// LocalAddresses lists local non-loopback IPs without a network lookup.
func LocalAddresses() ([]string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err == nil && ip.IsGlobalUnicast() {
			out = append(out, ip.String())
		}
	}
	if len(out) == 0 {
		return nil, &code.Error{Code: "join_address", Message: "the machine has no usable local address", Hint: "Use --address with an address that the laptop can reach."}
	}
	return out, nil
}
