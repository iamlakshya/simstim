//go:build !linux

package host

func verifyUserManagerNamespace(peer) bool { return false }
