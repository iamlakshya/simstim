package guest

import "os"

// fileCommitCommand is a private fixed-role descriptor set. Only the actual
// VM mount owner creates it. It cannot be supplied through CtlExecArgs, an
// upload header, a raw descriptor number or a caller success callback.
type fileCommitCommand struct {
	owner *Init
	files []*os.File
}

// VMFileCommitHelperMode is the closed root-entry/drop-to-agent publication
// role, separate from the fixed UID1000 put receiver and Git fd3 purpose.
const VMFileCommitHelperMode="wall-file-commit-v1"
