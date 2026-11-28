//go:build !linux

package chv

import "errors"

func kernelRelease() string { return "" }

func checkKVMRW(string) error {
	return errors.New("KVM is only available on Linux")
}
