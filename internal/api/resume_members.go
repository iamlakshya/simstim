package api

// MemberResumeArgs carries only one saved child's legacy approved values.
// Root identity and complete membership are pinned by SandboxSnapshot.
type MemberResumeArgs struct {
	ID      string            `json:"id"`
	Secrets map[string]Secret `json:"secrets,omitempty"`
}
