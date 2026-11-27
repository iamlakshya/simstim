//go:build !windows

package filecopy

func platformName(string) error { return nil }
