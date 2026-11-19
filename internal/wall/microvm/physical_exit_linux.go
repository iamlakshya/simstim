//go:build linux

package microvm

// PhysicalExitV1 reports only the SAME original process owner's native exit.
// It reuses the existing retained-pidfd refresh and grants no start, stop,
// source, admission, clean exit or state publication authority.
func (s *sandbox) PhysicalExitV1() bool { return s.refreshPhysicalExit() }
