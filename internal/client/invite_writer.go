package client

import "context"

// installInviteRegistrationCheck runs only inside changeHostFiles' original
// locked transaction. Its final check precedes the native hosts-file write;
// the existing pin and tier rollback handles a late expiry or cancellation.
func installInviteRegistrationCheck(ctx context.Context, change HostChange, hooks *hostFileHooks) {
	if _, ok := ctx.Value(inviteAcceptanceKey{}).(*InviteAcceptance); !ok {
		return
	}
	prior := hooks.beforeSave
	hooks.beforeSave = func() error {
		if prior != nil {
			if err := prior(); err != nil {
				return err
			}
		}
		return CheckInviteRegistrationV1(ctx, change)
	}
}
