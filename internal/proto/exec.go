package proto

// CtlExec registers a bounded one-shot command before its KindExec stream opens.
const (
	CtlExec          = "exec"
	CtlSpawn         = "spawn"
	CtlStopMember    = "stop_member"
	CtlMembersReady  = "members_ready"
	GuestPlainTestV1 = "plain-test-v1"
	GuestPTY1000V1   = "agent-pty1000-v1"
	GuestOwnedPTYV2  = "guest-owned-agent-pty-v2"
)

// CtlExecArgs are trusted supervisor choices, never an arbitrary client cwd/env.
type CtlExecArgs struct {
	ID        string   `json:"id"`
	Argv      []string `json:"argv"`
	Cwd       string   `json:"cwd"`
	TimeoutMS int64    `json:"timeout_ms"`
}

// ExecHeader binds exactly one stream to its registered command.
type ExecHeader struct {
	Purpose string `json:"purpose,omitempty"`
	ID      string `json:"id"`
}

// CtlMemberArgs selects a registered member, never a pathname or PID.
type CtlMemberArgs struct {
	Session string `json:"session"`
}
