package proto

// These operations are supervisor-to-init only. A client cannot select an
// executable, environment, credential, host path or process identifier.
const CtlEditorPrepareV1 = "editor.prepare-v1"
const CtlEditorCancelV1 = "editor.cancel-v1"

const (
	EditorTerminalV1 = "ssh-ephemeral-v1"
	EditorSFTPV1     = "ssh-sftp-v1"
	EditorExecV1     = "ssh-exec-v1"
)

type CtlEditorPrepareArgsV1 struct {
	Version int    `json:"v"`
	ID      string `json:"id"`
	Session string `json:"session"`
	Run     string `json:"run"`
	Purpose string `json:"purpose"`
	Command string `json:"command,omitempty"`
}

// EditorGuestHeaderV1 consumes exactly the registered operation on this
// existing guest link. Terminal attributes are data on that owned stream.
type EditorGuestHeaderV1 struct {
	Kind    string `json:"kind,omitempty"`
	Version int    `json:"v"`
	ID      string `json:"id"`
	Purpose string `json:"purpose"`
}

// Terminal attributes follow the small native-owned open as one bounded
// frame. Modes are binary bytes and use JSON base64, never UTF-8 replacement.
type EditorTerminalConfigV1 struct {
	Term  string `json:"term"`
	Cols  uint32 `json:"cols"`
	Rows  uint32 `json:"rows"`
	Modes []byte `json:"modes"`
}

type EditorReadyV1 struct {
	Version int    `json:"v"`
	Ready   bool   `json:"ready"`
	Error   *Error `json:"error,omitempty"`
}

type CtlEditorCancelArgsV1 struct {
	Version int    `json:"v"`
	ID      string `json:"id"`
}

// One control is either input EOF or an allowed signal, never a PID.
type EditorTerminalControlV1 struct {
	Signal   uint32 `json:"signal,omitempty"`
	InputEOF bool   `json:"input_eof,omitempty"`
}
