//go:build !linux

package guest

func maybeRunMicroVM([]string) (bool, error) { return false, nil }
