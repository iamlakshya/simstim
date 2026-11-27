package client

import "context"

// EndHostCreationAttemptContext irreversibly closes the original reservation's
// provider/result/publication phase. It grants no metadata/native cleanup and
// must run even when the caller has cancelled. Existing native ownership is
// released by its original release function, after this terminal transition.
func EndHostCreationAttemptContext(ctx context.Context) {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil {
		return
	}
	o.creation.finished.Store(true)
}
