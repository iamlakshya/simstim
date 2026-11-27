//go:build !linux

package host

import "os"

func captureWorktreeBirthV2(*os.Root) (*worktreeBirthV2, error) { return nil, nil }

func checkWorktreeBirthV2(*os.Root, *worktreeBirthV2) error {
	return sessionLeaseError("this worktree has no supported native filesystem generation producer")
}
