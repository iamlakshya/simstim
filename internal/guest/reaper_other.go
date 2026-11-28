//go:build !linux

package guest

func startReaper(func(int) bool) func() { return func() {} }
