//go:build !linux

package hosttest

func runVMHelper([]string) bool { return false }
