package client

import "context"

// This revalidates only an already held original start descriptor. It never
// opens settings, mints a request or touches a fixed cleanup continuation.
func revalidateOriginalStartSettingsV1(ctx context.Context, operation string) error {
	if operation != "new" && operation != "resume" {
		return nil
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	held := r.preparationSettings
	err := r.lifetimeErrorLocked()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if held != nil {
		return held.Revalidate(ctx)
	}
	return nil
}
