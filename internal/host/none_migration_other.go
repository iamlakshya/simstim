//go:build !linux

package host

import (
	"context"
	"time"
)

func nowUTCForNoneV1() time.Time           { return time.Now().UTC() }
func validNoneMigrationBootV1(string) bool { return false }
func captureLegacyNoneRebootV1(context.Context, *SessionOperation) error {
	return sessionLeaseError("legacy migration needs a supported host's original native process owner")
}
func stopLegacyForNoneResumeV1(context.Context, *SessionOperation) error {
	return sessionLeaseError("legacy migration needs a supported host's original native process owner")
}
