//go:build linux

package host

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/fsbirth"
)

// The resource-specific actual mkdir producer owns this wrapper. Shared
// fsbirth observes only kernel DATA and cannot construct a cleanup source.
func captureWorktreeBirthV2(root *os.Root) (*worktreeBirthV2, error) {
	data, err := fsbirth.Capture(root)
	if err != nil {
		return nil, errors.Join(placementError("the actual worktree filesystem generation could not be captured"), err)
	}
	return (*worktreeBirthV2)(data), nil
}

func checkWorktreeBirthV2(root *os.Root, birth *worktreeBirthV2) error {
	if !birth.knownV2() {
		return UserError("worktree_origin", "the worktree has no original native filesystem generation receipt", "Save this worktree and inspect its original creation and filesystem support before removing it by hand. Force does not establish ownership.")
	}
	actual, err := fsbirth.Capture(root)
	if err != nil {
		return err
	}
	if !fsbirth.Same(actual, (*fsbirth.DataV2)(birth)) {
		return UserError("worktree_origin", "the actual worktree directory differs from its original native creation generation", "Preserve this directory and inspect the original worktree creation record before retrying removal. Force does not establish ownership.")
	}
	return nil
}
