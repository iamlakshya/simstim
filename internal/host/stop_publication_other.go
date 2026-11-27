//go:build !linux

package host

import "context"

func publishRequestedStopV1(context.Context, *SessionOperation, *StoppedSessionV1) (*Manifest, error) {
	return nil, UserError("wall_unavailable", "native stop publication needs its original Linux lifetime owner", "Run stop on the sandbox's supported Linux host.")
}
