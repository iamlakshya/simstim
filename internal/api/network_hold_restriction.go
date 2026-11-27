package api

import "time"

// NetworkHoldMaximum is a restrictive ceiling from the trusted local request.
// It never supplies a private-address exception or permits a target by itself.
type NetworkHoldMaximum string

const (
	NetworkHoldPublicOnly     NetworkHoldMaximum = "public-only"
	NetworkHoldExistingPolicy NetworkHoldMaximum = "existing-address-policy"
)

func ValidNetworkHoldMaximum(maximum NetworkHoldMaximum) bool {
	return maximum == NetworkHoldPublicOnly || maximum == NetworkHoldExistingPolicy
}

// NetworkHoldTargetClass describes only a current target form. A DNS name's
// public class makes no promise about later DNS results or socket addresses.
// Sensitivity/redaction is independent and never follows from this class.
type NetworkHoldTargetClass string

const (
	NetworkHoldPublicTarget  NetworkHoldTargetClass = "public"
	NetworkHoldPrivateTarget NetworkHoldTargetClass = "private"
)

type NetworkHoldDescribeArgs struct {
	Version int    `json:"v"`
	Session string `json:"session"`
	Request string `json:"request"`
}

// NetworkHoldDescriptor is refusal/display metadata from a fresh private ID
// lookup. No host, secret target, source or address permission is exposed.
type NetworkHoldDescriptor struct {
	Version  int                    `json:"v"`
	Request  string                 `json:"request"`
	Revision uint64                 `json:"revision"`
	Class    NetworkHoldTargetClass `json:"class"`
	Reason   string                 `json:"reason,omitempty"`
	Expires  time.Time              `json:"expires"`
}

func ValidNetworkHoldDescriptor(descriptor NetworkHoldDescriptor, request string) bool {
	return ValidNetworkHoldReason(descriptor.Reason) && descriptor.Version == NetworkHoldV1 && ValidNetworkHoldID(request) && descriptor.Request == request && descriptor.Revision > 0 &&
		(descriptor.Class == NetworkHoldPublicTarget || descriptor.Class == NetworkHoldPrivateTarget) && !descriptor.Expires.IsZero() && descriptor.Expires.Year() >= 0 && descriptor.Expires.Year() <= 9999
}

// ValidNetworkHoldPositiveAction checks only the closed restrictive frame.
// The actual current private target and sticky maximum remain backend-owned.
func ValidNetworkHoldPositiveAction(args NetworkHoldActionArgs) bool {
	return args.Version == NetworkHoldV1 && ValidSessionID(args.Session) && ValidNetworkHoldID(args.Request) && args.Revision > 0 && ValidNetworkHoldMaximum(args.Maximum) &&
		(args.Scope == NetworkHoldOnce || args.Scope == NetworkHoldSession || args.Scope == NetworkHoldRepo || args.Scope == NetworkHoldProfile)
}
