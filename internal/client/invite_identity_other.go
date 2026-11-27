//go:build !unix && !windows

package client

import "os"

func joinedNativeIdentity(*os.File, uint64) (string, error) {
	return "", inviteError("invite_ownership", "this system cannot prove joined file ownership", "Use a supported client system to manage this host's local files.")
}
