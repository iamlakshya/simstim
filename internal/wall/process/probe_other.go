//go:build !linux

package process

func kernelRelease() string { return "" }
func landlockABI() int      { return 0 }
func seccompOK() bool       { return false }
