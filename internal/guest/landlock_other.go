//go:build !linux

package guest

func reexecAfterLandlock([]string) error { return nil }
