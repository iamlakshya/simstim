package proto

// CtlForkBranchesV1 renames only the original parent's branch namespace in
// a cold child's copied repositories, before an agent starts.
const CtlForkBranchesV1 = "fork.branches.v1"

// CtlForkBranchesArgsV1 contains session identities, never host paths.
type CtlForkBranchesArgsV1 struct {
	Parent string `json:"parent"`
	Child  string `json:"child"`
}
