//go:build linux

package gvisor

// PhysicalExitV1 asks only the original command owner. That owner retains
// both native process lifetimes and its namespace topology on uncertainty.
func (s *sandbox) PhysicalExitV1() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started && s.command != nil && s.physical && s.command.PhysicalExitV1()
}
