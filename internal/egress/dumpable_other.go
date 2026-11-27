//go:build !linux

package egress

import "github.com/iamlakshya/simstim/internal/secretmem"

// setUndumpable disables core files on other supported Unix systems.
// Linux dumpability has no equivalent flag there.
func setUndumpable() error { return secretmem.Protect() }

// Dumpable reports 0 outside Linux, where tests run the egress code in a
// helper process without prctl.
func Dumpable() (int, error) { return 0, nil }
