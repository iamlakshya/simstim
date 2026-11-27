package egress

import (
	"context"
	"net/http"
)

// The fixed Judge retains its original dial factory, Source check, endpoint,
// TLS trust and cancellation owner. Only its successful body-write prefixes
// replace the previous inspected-payload-size assumption.
func roundTripPhysicalJudgeOrigin(request *http.Request, template *http.Transport, turn *ModelUsageTurnV1, host string, check func(context.Context) bool) (*http.Response, *http.ClientConn, *physicalH2UploadConn, error) {
	if request == nil || request.URL == nil || template == nil || template.DialContext == nil || check == nil || !check(request.Context()) {
		return nil, nil, nil, errPhysicalH2Binding
	}
	if err := turn.beginPhysicalRequestV1(); err != nil {
		return nil, nil, nil, err
	}
	raw, err := template.DialContext(request.Context(), "tcp", request.URL.Host)
	if err != nil {
		return nil, nil, nil, err
	}
	observer, actual, err := physicalOriginUpload(request, template, raw, turn, host)
	if err != nil {
		_ = raw.Close()
		return nil, nil, nil, err
	}
	view := physicalClientView(template, actual, observer, request.URL.Host, request.URL.Scheme, check)
	client, err := view.NewClientConn(request.Context(), request.URL.Scheme, request.URL.Host)
	if err != nil {
		_ = observer.Close()
		return nil, nil, nil, err
	}
	response, err := client.RoundTrip(request)
	if err != nil {
		_ = observer.Close()
		_ = client.Close()
		return nil, nil, nil, err
	}
	if actual != nil {
		state := actual.ConnectionState()
		response.TLS = &state
	}
	response.Body = &physicalOriginBody{ReadCloser: response.Body, upload: observer}
	return response, client, observer, nil
}
