//go:build linux

package guest

func (v *vmRuntime) retireMemberForResume(id, previous string) error {
	v.mu.Lock()
	terminal := v.ptys[id]
	valid := terminal != nil && terminal.token == previous && terminal.run != nil && !terminal.starting
	v.mu.Unlock()
	if !valid {
		return vmError("the VM member has no matching original PTY run")
	}
	if err := v.stopMember(id); err != nil {
		return err
	}
	<-terminal.done
	if !terminal.run.waitKnown {
		return vmError("the previous VM member has no owned child exit proof")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ptys[id] != terminal || terminal.token != previous {
		return vmError("the VM member changed during its owned retirement")
	}
	delete(v.ptys, id)
	return nil
}
