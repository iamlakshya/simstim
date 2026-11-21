package proto

// CtlVMPrepare applies one closed VM boot configuration before seed/start.
const CtlVMPrepare = "vm_prepare"

// VMStartV1 contains resolved protected existing sandbox options. It is not
// another config source and contains no host path, PID, descriptor or key.
type VMStartV1 struct {
	Version             int      `json:"version"`
	System              string   `json:"system"`
	Sudo                bool     `json:"sudo"`
	Docker              bool     `json:"docker"`
	StateDirs           []string `json:"state_dirs"`
	RestoreCaptureNonce string   `json:"restore_capture_nonce,omitempty"`
}

// CtlVMPrepareArgs establishes workspace/state before the canonical seed.
// StateFiles/CaptureNonce keep the existing family plan/current-run fields.
type CtlVMPrepareArgs struct {
	Correlation  *VMPrepareCorrelationV1 `json:"correlation"`
	Session      string                  `json:"session"`
	VM           VMStartV1               `json:"vm"`
	StateFiles   []string                `json:"state_files"`
	CaptureNonce string                  `json:"capture_nonce"`
}

// CtlVMPrepareResult reports only the applied guest contract, never a host
// fact or readiness claim from an ordinary inherited-socket guest.
type CtlVMPrepareResult struct {
	Correlation *VMPrepareCorrelationV1 `json:"correlation"`
	GuestABI    string                  `json:"guest_abi"`
}

// EventMem carries guest-cgroup counters as claims, separately from host facts.
const EventMem = "mem"

// GuestMemoryV1 is the bounded v1 claim from the guest's agents cgroup.
// Events contains only low/high/max/oom/oom_kill/oom_group_kill counters.
type GuestMemoryV1 struct {
	Current int64             `json:"current"`
	Max     int64             `json:"max"`
	Events  map[string]uint64 `json:"events"`
}

// VMPrepareCorrelationV1 is bounded data projected from the actual native
// boot receipt. Its values cannot construct a receipt, grant or VM owner.
// The same original connection consumes it once and echoes the exact tuple.
type VMPrepareCorrelationV1 struct {
	Version    int    `json:"v"`
	Session    string `json:"session"`
	Generation string `json:"generation"`
	NeedSHA256 string `json:"need_sha256"`
	Nonce      string `json:"nonce"`
}
