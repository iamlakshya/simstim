//go:build !linux

package guest

func checkNamedSSHHelperProjectionV1() error {
	return execError("the named SSH agent requires a Linux guest helper projection")
}
