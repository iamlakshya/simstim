//go:build !darwin && !linux

package client

import (
	"context"
	"os"
)

func makeAgentNativeLog(context.Context) (*agentNativeLog, error) {
	return nil, agentNativeLogError("this platform has no supported private FIFO capture")
}

func agentNativeOwnedInfo(os.FileInfo, os.FileMode) bool { return false }

func (*agentNativeLog) rawReadLocked([]byte) (int, error) {
	return 0, agentNativeLogError("this platform has no supported private FIFO capture")
}

func (*agentNativeLog) unreadLocked() (int, error) {
	return 0, agentNativeLogError("this platform has no supported private FIFO capture")
}

func (*agentNativeLog) pollReader() error {
	return agentNativeLogError("this platform has no supported private FIFO capture")
}

func newAgentModeDirectory() (*agentModeDirectory, error) { return nil, unsupportedLocalAgent() }
func makeAgentNativeLogAtDirectory(context.Context, *agentModeDirectory, bool) (*agentNativeLog, error) {
	return nil, unsupportedLocalAgent()
}
func agentIndexOwnedInfo(os.FileInfo, uint64) bool { return false }
func (*agentIndexLease) create() error             { return unsupportedLocalAgent() }
func (*agentIndexLease) removePending() error      { return unsupportedLocalAgent() }
