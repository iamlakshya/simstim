package proto

const (
	FeatureNativeCheckpointCustodyV1              = "native-checkpoint-custody-v1"
	NativeCheckpointCustodyV1                     = "checkpoint.custody-v1"
	NativeCheckpointCustodyRequestV1       uint64 = 24
	NativeCheckpointRoleCountV1                   = 17
	FeatureNativeCheckpointReservationV2          = "native-checkpoint-reservation-v2"
	NativeCheckpointReservationV2                 = "checkpoint.reservation-v2"
	NativeCheckpointReservationRequestV2   uint64 = 26
	NativeCheckpointReservationRoleCountV2        = 18
)

type NativeCheckpointCustodyResultV1 struct {
	State NativeStopStateV1 `json:"state"`
	Mode  string            `json:"mode"`
}

type NativeCheckpointReservationResultV2 struct {
	State NativeStopStateV1 `json:"state"`
	Mode  string            `json:"mode"`
}

const (
	FeatureNativeSlotReservedStartupV2               = "native-slot-reserved-startup-v2"
	NativeSlotReservedStartupPrepareV2               = "slot.startup.prepare-v2"
	NativeSlotReservedStartupLoanV2                  = "slot.startup.loan-v2"
	NativeSlotReservedStartupPrepareRequestV2 uint64 = 27
	NativeSlotReservedStartupLoanRequestV2    uint64 = 28
	NativeSlotReservedStartupRoleCountV2             = 19
)
