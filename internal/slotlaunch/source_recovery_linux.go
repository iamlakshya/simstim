//go:build linux

package slotlaunch

import "os"

// orphanRecoveryNeeded permits the existing Stop operation only after the
// original keeper's kernel birth handle positively reports exit. The native
// cgroup and parent must still be the exact objects captured at adoption.
func (s *NativeSourceV1) orphanRecoveryNeeded() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.adopted || s.owner == nil || len(s.files) != 13 || !pidExited(s.files[0]) {
		return false, nil
	}
	boot, err := nativeBootID()
	if err != nil || boot != s.boot {
		return false, refused("the exited keeper's native source belongs to another boot")
	}
	if err := sameNativeCgroupParent(s.files[2], s.parent); err != nil {
		return false, err
	}
	root, err := cgroupRoot()
	if err != nil {
		return false, err
	}
	defer root.Close()
	current, err := openCgroup(root, s.identity)
	if err != nil {
		return false, err
	}
	defer current.Close()
	actual, err := current.Stat()
	if err != nil {
		return false, err
	}
	held, err := s.files[2].Stat()
	if err != nil || !os.SameFile(actual, held) {
		return false, refused("the exited keeper's original native cgroup changed")
	}
	live, err := populated(s.files[2])
	if err != nil {
		return false, err
	}
	return live, nil
}
