package api

// PurgeCleanupV1 is a closed transient payload, never an installation plan.
// It can read the original completed report or acknowledge its saved guard.
type PurgeCleanupV1 struct {
	Version int                `json:"v"`
	Install InstallPlanV1      `json:"carried_helper"`
	HostID  string             `json:"expected_host_id"`
	Nonce   string             `json:"operation_nonce"`
	Action  string             `json:"action"`
	Witness *PurgeGuardWitness `json:"witness,omitempty"`
}
