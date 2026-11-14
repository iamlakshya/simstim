package client

import (
	"net"
	"os"
)

func newAgentCollectorSocket() (*agentCollectorSocket, error) { return nil, unsupportedLocalAgent() }
func agentCollectorOwnUID(uint32) bool                        { return false }
func agentCollectorPeer(net.Conn) (int, uint32, error)        { return 0, 0, unsupportedLocalAgent() }

func agentCollectorSocketOwned(os.FileInfo) bool    { return false }
func agentCollectorDirectoryOwned(os.FileInfo) bool { return false }

func newAgentCollectorSocketAtDirectory(*agentModeDirectory) (*agentCollectorSocket, error) {
	return nil, unsupportedLocalAgent()
}
