//go:build !linux && !darwin && !windows

package client

import (
	"io"
	"time"
)

func localGitCancelGrace() time.Duration { return 0 }

func startLocalGitOwner(localGitRequest, io.Reader, io.Writer, io.Writer) (*localGitOwner, error) {
	return nil, &UserError{Code: "git_ownership", Msg: "this platform has no verified local Git process owner", Hint: "Use a supported client platform with a verified native process owner.", Kind: KindRefused}
}
