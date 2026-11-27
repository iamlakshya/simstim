package supervisor

import "slices"

func (a *namedSSHAgent) names() []string {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.ctx.Err() != nil {
		return nil
	}
	names := make([]string, 0, len(a.keys))
	for _, key := range a.keys {
		names = append(names, key.name)
	}
	slices.Sort(names)
	return names
}

func (s *Supervisor) namedSSHKeyNames() []string {
	root := s.owner()
	root.mu.Lock()
	agent := root.sshAgent
	root.mu.Unlock()
	return agent.names()
}
