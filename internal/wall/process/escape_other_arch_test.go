//go:build linux && !amd64

package process

func int80Unshare() {}

func supportsInt80Check() bool { return false }
