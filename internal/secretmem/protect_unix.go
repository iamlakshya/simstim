//go:build unix

package secretmem

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Protect runs before the first secret-bearing read. Hard zero remains
// inherited, including across exec. Each trusted executable still sets its
// own Linux dumpability flag because exec can reset that flag.
func Protect() error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("disable process core files: %w", err)
	}
	return setNonDumpable()
}

// Inspect reads actual state without changing a limit or another process.
func Inspect() (State, error) {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &limit); err != nil {
		return State{}, fmt.Errorf("read process core limits: %w", err)
	}
	dumpable, err := currentDumpable()
	if err != nil {
		return State{}, err
	}
	return State{Dumpable: dumpable, CoreSoft: limit.Cur, CoreHard: limit.Max}, nil
}
