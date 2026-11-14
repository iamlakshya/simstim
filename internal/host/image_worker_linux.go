//go:build linux

package host

import "golang.org/x/sys/unix"

func imageWorkerConfinement() error { return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) }
