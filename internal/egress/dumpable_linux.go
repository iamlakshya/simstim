//go:build linux

package egress

import "github.com/iamlakshya/simstim/internal/secretmem"

// setUndumpable marks the egress process as not dumpable before it reads
// traffic or secrets, so another process of the same uid cannot ptrace it
// or read its core dump (spec 5.6, P1.7).
func setUndumpable() error {
	return secretmem.Protect()
}

// Dumpable reports the current process's PR_GET_DUMPABLE flag.
func Dumpable() (int, error) {
	state, err := secretmem.Inspect()
	if err != nil {
		return 0, err
	}
	return *state.Dumpable, nil
}
