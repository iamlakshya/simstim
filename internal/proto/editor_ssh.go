package proto

const FeatureEditorSSHV1 = "editor-ssh-v1"
const EditorSSHOpenV1 = "ssh.open"

const EditorSSHCommandMaxV1 = 32 << 10

// EditorSSHOpenArgsV1 names only one session. It is DATA: only the actual
// accepted peer and its original native controller can issue its live owner.
type EditorSSHOpenArgsV1 struct {
	Version int    `json:"v"`
	Session string `json:"session"`
}

type EditorSSHOpenResultV1 struct {
	Version int    `json:"v"`
	Session string `json:"session"`
	Root    string `json:"root"`
}

// EditorSSHViewV1 is display data from the live selected supervisor only.
type EditorSSHViewV1 struct {
	Version int  `json:"v"`
	Open    bool `json:"open"`
}

// EditorChannelV1 is the closed header of one child on the SAME carrier.
// It never supplies environment, credential, role, path or native permission.
type EditorChannelV1 struct {
	Version int    `json:"v"`
	Command string `json:"command,omitempty"`
	Port    uint32 `json:"port,omitempty"`
	Term    string `json:"term,omitempty"`
	Cols    uint32 `json:"cols,omitempty"`
	Rows    uint32 `json:"rows,omitempty"`
	Modes   []byte `json:"modes,omitempty"`
}

// EditorSignalNumberV1 uses the Linux guest ABI on every client platform.
// Laptop syscall numbers differ (notably USR1/USR2 on macOS) and are never
// forwarded. This is closed request data, not permission to target a PID.
func EditorSignalNumberV1(name string) (uint32, bool) {
	switch name {
	case "ABRT":
		return 6, true
	case "ALRM":
		return 14, true
	case "FPE":
		return 8, true
	case "HUP":
		return 1, true
	case "ILL":
		return 4, true
	case "INT":
		return 2, true
	case "KILL":
		return 9, true
	case "PIPE":
		return 13, true
	case "QUIT":
		return 3, true
	case "SEGV":
		return 11, true
	case "TERM":
		return 15, true
	case "USR1":
		return 10, true
	case "USR2":
		return 12, true
	default:
		return 0, false
	}
}
