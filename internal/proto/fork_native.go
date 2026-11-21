package proto

// The native fork lane reuses the actual outside source inspection and its
// retained incoming operation. The payloads correlate original descriptor
// loans; they carry no file name or path authority.
const (
	NativeForkDestinationV1               = "lifetime.fork-destination-v1"
	NativeForkCopyV1                      = "lifetime.fork-copy-v1"
	NativeForkDestinationRequestV1 uint64 = 20
	NativeForkCopyRequestV1        uint64 = 21
)

type NativeForkDestinationArgsV1 struct {
	Version int              `json:"v"`
	Scope   NativeStopArgsV1 `json:"scope"`
	Child   string           `json:"child"`
}

type NativeForkCopyArgsV1 struct {
	Version       int              `json:"v"`
	Scope         NativeStopArgsV1 `json:"scope"`
	SourceSession string           `json:"source_session"`
	Children      []string         `json:"children"`
}

// AgentSessionID is bounded claim data captured while all source writers are
// frozen. These fields grant neither source nor boot rights.
type NativeForkCopyResultV1 struct {
	State          NativeStopStateV1 `json:"state"`
	AgentKind      string            `json:"agent_kind"`
	AgentSessionID string            `json:"agent_session_id,omitempty"`
}
