package client

import "context"

// PrepareHostRegistrationBaselineV1 performs the actual first fixed native
// baseline read under the original registration, before helper setup. Only
// this real route producer binds the transport object accepted by the reader.
func PrepareHostRegistrationBaselineV1(ctx context.Context, h Host, interactive bool) (Transport, *BaselineCapture, error) {
	observation, err := captureHostStateObservation(ctx, h)
	if err != nil {
		return nil, nil, err
	}
	if observation.role != hostStateRegistration {
		return nil, nil, baselineErrorV1("the baseline needs the original host registration")
	}
	owned, finish, attempt, err := beginHostTransportObservation(ctx, h)
	if err != nil {
		return nil, nil, err
	}
	defer finish()
	if err := checkHostTransportAttempt(owned, attempt, h); err != nil {
		return nil, nil, err
	}
	route, err := routeTo(owned, h, interactive)
	if err != nil {
		return nil, nil, err
	}
	if err := checkHostTransportAttempt(owned, attempt, h); err != nil {
		return nil, nil, err
	}
	if err := pinRoute(owned, h, route.t, interactive); err != nil {
		return nil, nil, err
	}
	attempt.registrationMu.Lock()
	if route.t == nil || attempt.registrationTransport != nil {
		attempt.registrationMu.Unlock()
		return nil, nil, baselineErrorV1("the original registration already chose another baseline route")
	}
	attempt.registrationTransport = route.t
	attempt.registrationMu.Unlock()
	baseline, err := CaptureHostBaseline(owned, h, route.t)
	if err != nil {
		return nil, nil, err
	}
	if err := checkHostTransportAttempt(owned, attempt, h); err != nil {
		return nil, nil, err
	}
	return route.t, baseline, nil
}

func (a *hostTransportAttempt) matchesRegistrationTransportV1(transport Transport) bool {
	if a == nil || !a.active.Load() {
		return false
	}
	a.registrationMu.Lock()
	defer a.registrationMu.Unlock()
	return sameHostRuntimeObject(a.registrationTransport, transport)
}
