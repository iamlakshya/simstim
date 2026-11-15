package api

import (
	"fmt"
	"strconv"
)

// DoorPort is an explicitly granted forward to one guest loopback port.
const DoorPort Door = "port"

// FeatureSandboxPortsV1 offers checked loopback forwards and claimed port lists.
const FeatureSandboxPortsV1 = "sandbox-ports-v1"

// FeatureReversePortsV1 offers one own-host reverse forward per connection.
const FeatureReversePortsV1 = "sandbox-reverse-ports-v1"

// PortArgsV1 names one session and one exact guest port. It grants no laptop
// destination; a reverse client keeps that destination in its own request.
type PortArgsV1 struct {
	Version int    `json:"v"`
	ID      string `json:"id"`
	Port    int    `json:"port"`
}

// PortGrantResultV1 returns only the trusted supervisor's private Unix socket.
type PortGrantResultV1 struct {
	Version int    `json:"v"`
	Port    int    `json:"port"`
	Socket  string `json:"socket"`
}

// ParsePortTarget accepts one canonical decimal TCP port, without aliases.
func ParsePortTarget(target string) (int, error) {
	port, err := strconv.Atoi(target)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != target {
		return 0, fmt.Errorf("port must be a decimal integer from 1 to 65535")
	}
	return port, nil
}

// PortForwardInfo is display-only state from an actual live forward owner.
// It is never a grant, a guest destination selector or local tier authority.
type PortForwardInfo struct {
	Port    int  `json:"port"`
	Reverse bool `json:"reverse,omitempty"`
}

// PortForwardViewV1 is a live supervisor snapshot, never persisted in a manifest.
type PortForwardViewV1 struct {
	Version  int               `json:"v"`
	Forwards []PortForwardInfo `json:"forwards"`
}
