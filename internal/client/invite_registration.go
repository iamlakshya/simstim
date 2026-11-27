package client

import "context"

// RegistrationContextV1 scopes H5's existing actual writer capture to this
// original invitation's registration only. It grants no transport or save.
func (a *InviteAcceptance) RegistrationContextV1() (context.Context, error) {
	if a == nil || a.unlock == nil || a.consumed || a.stage == nil || a.expected.Name != a.original.Name {
		return nil, hostStateRemovalError("the invite registration has no original active writer stage")
	}
	if err := a.checkOriginalInviteV1(a.Context()); err != nil {
		return nil, err
	}
	if a.capture != nil {
		return nil, hostStateRemovalError("the original invite registration capture already started")
	}
	a.capture = &hostPrepareCommitCapture{name: a.original.Name}
	return context.WithValue(a.Context(), hostPrepareCommitCaptureKey{}, a.capture), nil
}

// RegistrationTargetV1 returns only the opaque target made by that original
// successful native writer callback. It performs no name lookup or promotion.
func (a *InviteAcceptance) RegistrationTargetV1() (*HostPrepareTargetV1, error) {
	if a == nil || a.capture == nil || a.capture.target == nil {
		return nil, hostStateRemovalError("the actual invite registration writer returned no saved Source")
	}
	target := a.capture.target
	if !target.FromRegistration() || target.Name() != a.original.Name || !SameEntry(target.Host(), a.expected) {
		return nil, hostStateRemovalError("the actual invite registration writer returned another target")
	}
	return target, nil
}

// retainInviteRegistrationCommitV1 is called only at the start of H5's actual
// post-write callback. Retention decreases cleanup; it grants no saved Source.
// A later capture failure must not remove files the writer already referenced.
func retainInviteRegistrationCommitV1(ctx context.Context, change HostChange, out []Host) error {
	a, _ := ctx.Value(inviteAcceptanceKey{}).(*InviteAcceptance)
	if a == nil {
		return nil
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	capture, _ := ctx.Value(hostPrepareCommitCaptureKey{}).(*hostPrepareCommitCapture)
	if capture == nil || capture.name != a.original.Name || a.stage == nil || change.Was != nil || change.Now == nil || !SameEntry(*change.Now, a.expected) {
		return hostStateRemovalError("the committed invite writer changed its original target")
	}
	found := false
	for _, host := range out {
		if host.Name == a.original.Name && SameEntry(host, a.expected) {
			found = true
			break
		}
	}
	if !found {
		return hostStateRemovalError("the actual committed hosts output has no matching invite")
	}
	a.stage.Commit()
	return nil
}
