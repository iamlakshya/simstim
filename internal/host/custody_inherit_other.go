//go:build !linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/proto"
)

type custodyInheritanceV1 struct{ native *sessionFileLease }

func inheritManagerCustodyV1(context.Context, string) (*custodyInheritanceV1, bool, error) {
	return nil, false, nil
}
func (*custodyInheritanceV1) Close() error { return nil }

// ManagerCustodyPendingV1 has no inherited kernel producer on this platform.
func (*OwnedSupervisorLease) ManagerCustodyPendingV1() bool { return false }

// CheckManagerCustodyRequesterV1 has no native custody borrower here.
func (*OwnedSupervisorLease) CheckManagerCustodyRequesterV1(context.Context) error {
	return sessionLeaseError("native manager custody requires a Linux service main")
}

// HandleManagerCustodyBootstrapV1 cannot originate custody on this platform.
func (*OwnedSupervisorLease) HandleManagerCustodyBootstrapV1(context.Context, *StartupLineV1, proto.Frame) (bool, error) {
	return false, sessionLeaseError("native manager custody requires a Linux service main")
}
