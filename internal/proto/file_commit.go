package proto

// VMFileCommitV1 is private fixed-role correlation. The native VM resource
// producer sends its original mount handles separately. These strings and
// header data cannot construct those handles or authorise publication.
type VMFileCommitV1 struct {
	Version int `json:"v"`
	Mount string `json:"mount"`
	Header FilePutHeaderV1 `json:"header"`
}

// VMFileCommitOperationV1 belongs only to the private root/drop-to-agent
// process channel. It is not a bridge, ctl or guest-link operation.
const VMFileCommitOperationV1="vm_file_commit_v1"
