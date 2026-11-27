package jail

import (
	"os"
	"slices"
	"sort"
)

// FileRoleV1 is the closed purpose of a regular inherited-file grant.
// It is not a release role or a claim of byte provenance.
type FileRoleV1 string

// The v1 roles keep writable data separate from executable programs.
const (
	FileDataV1    FileRoleV1 = "data"
	FileProgramV1 FileRoleV1 = "program"
)

// FileRuleV1 grants inode rights to one captured final Files slot. FD is its
// child number, starting at 3, never a caller's native descriptor number.
// Mode is the expected exact permissions; no type or special bits are valid.
// These grants do not validate content or authorise a runtime role.
type FileRuleV1 struct {
	FD     int         `json:"fd"`
	Role   FileRoleV1  `json:"role"`
	Access Access      `json:"access"`
	Mode   os.FileMode `json:"mode"`
}

func checkFileRulesV1(in []FileRuleV1, files int) ([]FileRuleV1, error) {
	if files < 0 || files > int(^uint(0)>>1)-3 {
		return nil, failure("invalid inherited-file vector", "Give Spec.Files only the command's open inherited files.")
	}
	out := slices.Clone(in)
	sort.Slice(out, func(i, j int) bool { return out[i].FD < out[j].FD })
	for i, rule := range out {
		if rule.FD < 3 || rule.FD-3 >= files || i > 0 && out[i-1].FD == rule.FD {
			return nil, failure("an inherited-file rule names an invalid or duplicate Files slot", "Name each selected child Files slot once, starting at fd 3.")
		}
		if rule.Mode&^os.FileMode(0o777) != 0 || rule.Access == 0 || rule.Access&^(Read|Write|Exec) != 0 {
			return nil, failure("an inherited-file rule has unknown rights or mode bits", "Give exact regular-file permissions and only Read, Write or Exec.")
		}
		switch rule.Role {
		case FileDataV1:
			if rule.Access&Exec != 0 || rule.Mode&0o111 != 0 || rule.Access&Read != 0 && rule.Mode&0o444 == 0 || rule.Access&Write != 0 && rule.Mode&0o222 == 0 {
				return nil, failure("a data file rule conflicts with its mode or requests execution", "Grant only the actual data file's read or write access.")
			}
		case FileProgramV1:
			if rule.Access&Exec == 0 || rule.Access&Write != 0 || rule.Mode&0o111 == 0 || rule.Mode&0o022 != 0 {
				return nil, failure("a program file rule is writable or has no execution mode", "Grant Exec and optional Read on the exact program, without Write.")
			}
		default:
			return nil, failure("an inherited-file rule has an unknown role", "Use the version1 data or program role.")
		}
	}
	return out, nil
}
