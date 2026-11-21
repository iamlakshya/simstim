package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

// This protected creation record describes a kernel-issued persistent file
// handle. Inode/mtime/path data alone never supplies a directory generation.
// A consumer still opens its actual native directory and compares a fresh
// kernel handle before taking cleanup custody.
type worktreeBirthV2 struct {
	Version    int      `json:"v"`
	Filesystem int64    `json:"filesystem"`
	FSID       [2]int32 `json:"fsid"`
	Type       int32    `json:"type"`
	Handle     []byte   `json:"handle"`
}

func (birth *worktreeBirthV2) UnmarshalJSON(raw []byte) error {
	type plain worktreeBirthV2
	var decoded plain
	if !api.ExactJSONFields(raw, decoded) || json.Unmarshal(raw, &decoded) != nil {
		return placementError("the worktree native birth record contains unknown, duplicate or aliased fields")
	}
	var fields map[string]json.RawMessage
	var identity []int32
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(fields["fsid"], &identity) != nil || len(identity) != 2 {
		return placementError("the worktree native filesystem identity has an unknown fixed size")
	}
	*birth = worktreeBirthV2(decoded)
	return nil
}

func (birth *worktreeBirthV2) knownV2() bool {
	return birth != nil && birth.Version == 2 && birth.Filesystem != 0 && birth.FSID != [2]int32{} && birth.Type > 0 && len(birth.Handle) > 0 && len(birth.Handle) <= 128
}
