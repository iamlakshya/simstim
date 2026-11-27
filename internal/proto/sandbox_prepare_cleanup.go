package proto

// SandboxPrepareCleanupResultV1 is diagnostic DATA on the original private
// fixed-test cancellation reply. It supplies no native cleanup or start right.
// The receiver still requires the actual original creation and unit to join.
type SandboxPrepareCleanupResultV1 struct {
	Version    int    `json:"v"`
	Diagnostic *Error `json:"diagnostic"`
}

// SandboxPrepareCleanupResultMaxV1 bounds this private diagnostic result.
const SandboxPrepareCleanupResultMaxV1 = 32 << 10

// SandboxPrepareCleanupDiagnosticMaxV1 bounds the original close error text.
const SandboxPrepareCleanupDiagnosticMaxV1 = 4096
