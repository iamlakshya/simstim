//go:build linux

package probetest

import "golang.org/x/sys/unix"

func prepareDumpBaseline() string {
	if unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0) != nil {
		return "dumpable_baseline_not_permitted"
	}
	return "dumpable1_prepared"
}
