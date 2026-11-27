//go:build !linux && !darwin && !windows

package client

import "net"

func agentCollectorPeer(net.Conn) (int, uint32, error) { return 0, 0, agentBindingProofError() }
