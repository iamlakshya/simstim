//go:build windows

package cachefs

func safeNativeLeaf(name string) error { return safeLeaf(name) }
