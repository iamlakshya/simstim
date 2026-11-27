package proto

// GuestNonePTYV1 keeps the host account and namespaces on the ordinary link.
// It is distinct from both a UID1000 guest and the explicit plain fixture.
const GuestNonePTYV1 = "host-account-pty-v1"

const FeatureNoneSupervisorV1 = "none-supervisor-v1"
const FeatureNoneStopCompletionV1 = "none-stop-completion-v1"

const (
	NativeNonePrepareV1               = "lifetime.none-prepare-v1"
	NativeNoneCommitV1                = "lifetime.none-commit-v1"
	NativeNonePrepareRequestV1 uint64 = 30
	NativeNoneCommitRequestV1  uint64 = 31
)
