package client

// Close releases only this logical request's original local source/captures.
// Busy or incomplete cleanup retains ownership for the caller to join/retry.
func (r *GrantRequest) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.ending = true
	p, operation, inputs, fresh, imageSave := r.newSecrets, r.preparationSettings, r.newInputs, r.freshChild, r.imageSave
	policyReplace := r.policyReplace
	imageFetch := r.imageFetch
	sandboxPrepare := r.sandboxPrepare
	networkHold := r.networkHold
	sdkStart := r.sdkStart
	purge, purgeUndo := r.purge, r.purgeUndo
	judge := r.trustedJudge
	arm := r.armSecrets
	namedNew, namedResume, namedPolicy := r.namedNew, r.namedResume, r.namedPolicy
	rewind := r.rewind
	r.mu.Unlock()
	if err := sdkStart.close(); err != nil {
		return err
	}
	if err := networkHold.close(); err != nil {
		return err
	}
	if err := imageFetch.close(); err != nil {
		return err
	}
	if err := sandboxPrepare.close(); err != nil {
		return err
	}
	if err := rewind.Close(); err != nil {
		return err
	}
	if err := namedResume.close(); err != nil {
		return err
	}
	if err := namedPolicy.close(); err != nil {
		return err
	}
	if err := purgeUndo.close(); err != nil {
		return err
	}
	if err := purge.close(); err != nil {
		return err
	}
	if err := arm.close(); err != nil {
		return err
	}
	if err := judge.close(); err != nil {
		return err
	}
	if err := policyReplace.close(); err != nil {
		return err
	}
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed {
			return nil
		}
		if p.busy {
			return agentModeResourceError()
		}
	}
	if err := fresh.close(); err != nil {
		return err
	}
	if err := imageSave.close(); err != nil {
		return err
	}
	if err := r.closeLocalPurposeResources(); err != nil {
		return err
	}
	if err := inputs.close(); err != nil {
		return err
	}
	if p == nil {
		namedNew.close()
	}
	if operation != nil {
		if err := operation.Close(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.preparationClosed = true
	r.mu.Unlock()
	if p != nil {
		p.closed = true
		p.values = nil
		p.namedKeys.close()
	}
	return nil
}
