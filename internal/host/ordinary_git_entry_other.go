//go:build !linux

package host

func runOrdinaryGitWorkerEntryV2([]string) (bool, error) { return false, nil }
