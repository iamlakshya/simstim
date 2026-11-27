package proto

// CtlGuestCapabilities queries the actual guest's closed control capabilities.
const CtlGuestCapabilities = "guest.capabilities"

// CtlPrepare initialises session infrastructure without launching an agent.
const CtlPrepare = "session.prepare"

// GuestSessionPrepareV1 guards automatic controller-only restoration.
const GuestSessionPrepareV1 = "session-prepare-v1"

// CtlGuestFeatures is the actual guest's versioned control capability receipt.
type CtlGuestFeatures struct {
	Version  int      `json:"version"`
	Features []string `json:"features"`
}

// CtlPrepareArgs contains only the trusted controller's infrastructure input.
// Start has no argv, hooks or values; it cannot be an implicit agent launch.
type CtlPrepareArgs struct {
	Version int          `json:"version"`
	Start   CtlStartArgs `json:"start"`
}

// CtlPrepareResult acknowledges actual common session preparation, not a PTY.
type CtlPrepareResult struct {
	Version  int    `json:"version"`
	GuestABI string `json:"guest_abi"`
}
