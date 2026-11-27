package proto

import "encoding/json"

// These private helper-to-supervisor operations correlate a physical stop
// with this exact live native owner. Their payloads are data, never receipts.
const (
	FeatureNativeStopV1 = "native-stop-v1"
	NativeStopInspectV1 = "lifetime.inspect-v1"
	NativeStopCommitV1  = "lifetime.stop-v1"
)

type NativeStopArgsV1 struct {
	Version int    `json:"v"`
	Primary string `json:"primary"`
	Nonce   string `json:"nonce"`
}
type NativeStopStateV1 struct {
	Version    int    `json:"v"`
	Primary    string `json:"primary"`
	Nonce      string `json:"nonce"`
	Generation string `json:"generation"`
	Need       string `json:"need"`
	Runtime    string `json:"runtime"`
}

const NativeWarmResumeV1 = "lifetime.resume-agent-v1"

type NativeWarmResumeArgsV1 struct {
	Version int              `json:"v"`
	Scope   NativeStopArgsV1 `json:"scope"`
	Request json.RawMessage  `json:"request"`
}

const (
	FeatureNativeResumeV1 = "native-resume-v1"
	NativeResumeInspectV1 = "lifetime.inspect-resume-v1"
	NativeResumeCheckV1   = "lifetime.check-resume-v1"
)

const (
	NativeWarmRoleV1    = "native-warm-resume-v1"
	NativeWarmLoanV1    = "lifetime.resume-loan-v1"
	NativeWarmCancelV1  = "lifetime.resume-cancel-v1"
	NativeWarmReleaseV1 = "lifetime.resume-release-v1"
)
