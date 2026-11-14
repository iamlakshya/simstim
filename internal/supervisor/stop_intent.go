package supervisor

// markStopIntent runs before the controller sends Stop/System to the guest.
// It never overwrites an End already observed from an individually ended agent.
// Existing protected Life.End.Kind and final Status preserve restore intent;
// no guest assertion or new persistent authority field is used.
func (s *Supervisor) markStopIntent() {
	root := s.owner()
	root.mu.Lock()
	root.stopIntent = true
	root.mu.Unlock()
	root.membersMu.Lock()
	members := make([]*Supervisor, 0, len(root.launches))
	for _, member := range root.launches {
		members = append(members, member)
	}
	root.membersMu.Unlock()
	for _, member := range members {
		member.mu.Lock()
		member.stopIntent = true
		member.mu.Unlock()
	}
}
