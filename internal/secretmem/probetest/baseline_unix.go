//go:build unix

package probetest

import "golang.org/x/sys/unix"

func prepareCoreBaseline() string {
	var current unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_CORE, &current) != nil {
		return "getrlimit_failed"
	}
	maximum := current.Max
	if maximum == 0 {
		maximum = 64 << 10
	}
	soft := uint64(64 << 10)
	if maximum < soft {
		soft = maximum
	}
	if unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: soft, Max: maximum}) != nil {
		return "positive_baseline_not_permitted"
	}
	return "positive_baseline_prepared"
}
