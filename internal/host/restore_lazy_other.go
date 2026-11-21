//go:build !linux

package host

func stopLazyRestoreV1(*SessionOperation) error {
	return UserError("wall_unavailable", "lazy sandbox restore needs the Linux native lifetime owner", "Run restore on the sandbox's Linux host.")
}
