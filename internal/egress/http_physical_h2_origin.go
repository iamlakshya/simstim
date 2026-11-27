package egress

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/iamlakshya/simstim/internal/api"
)

// The original request owner selects this plaintext observer after its checked
// dial and verified TLS handshake. Both negotiated HTTP versions keep the same
// native connection, permit, Source, upload guard and completion finaliser.
func (t requestHTTPTransport) roundTripPhysicalOrigin(request *http.Request) (*http.Response, error) {
	if err := t.validate(request); err != nil {
		return nil, err
	}
	if t.server.transport.Proxy != nil || t.server.transport.TLSClientConfig == nil {
		return nil, errPhysicalH2Binding
	}
	t.scope.mu.Lock()
	model := t.scope.model
	fresh := t.scope.conn == nil && t.scope.client == nil && !t.scope.closed
	t.scope.mu.Unlock()
	if !fresh || model == nil || model.usage == nil || model.webSocket || !modelUsageInferencePathV1(modelUsageWire(model), model.request.Path) {
		return nil, errPhysicalH2Binding
	}
	if err := model.usage.beginPhysicalRequestV1(); err != nil {
		return nil, err
	}
	// Exactly the original host/address/permit/source path performs the dial.
	// HTTP metadata cannot name a second endpoint through this observer.
	raw, err := t.server.dialHTTPRequest(request.Context(), "tcp", request.URL.Host)
	if err != nil {
		return nil, err
	}
	t.scope.mu.Lock()
	bound := !t.scope.closed && t.scope.conn == raw && t.scope.model == model
	t.scope.mu.Unlock()
	if !bound || !t.scope.allowed(t.server.currentEngine(), t.server.dialer) {
		_ = raw.Close()
		return nil, errPhysicalH2Binding
	}
	observer, actual, err := physicalOriginUpload(request, t.server.transport, raw, model.usage, t.scope.request.Host)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	view := physicalClientView(t.server.transport, actual, observer, request.URL.Host, request.URL.Scheme, func(ctx context.Context) bool {
		return ctx.Value(originRequestKey{}) == t.scope && t.scope.ctx.Err() == nil
	})
	conn, err := view.NewClientConn(request.Context(), request.URL.Scheme, request.URL.Host)
	if err != nil {
		_ = observer.Close()
		return nil, err
	}
	owned := &ownedHTTPClientConn{&ownedHTTPClientConnState{conn: conn}}
	t.scope.mu.Lock()
	if t.scope.closed || t.scope.ctx.Err() != nil || t.scope.client != nil {
		t.scope.mu.Unlock()
		owned.close()
		_ = observer.Close()
		return nil, errPhysicalH2Binding
	}
	t.scope.client, t.scope.physicalUpload = owned, observer
	t.scope.mu.Unlock()
	response, err := conn.RoundTrip(request)
	if err != nil {
		owned.close()
		_ = observer.Close()
		return nil, err
	}
	if actual != nil {
		state := actual.ConnectionState()
		response.TLS = &state
	}
	response.Body = &physicalOriginBody{ReadCloser: response.Body, upload: observer}
	return response, nil
}

// EOF/error/Close joins request-side native write callbacks before the
// existing model owner can finish a cut or complete answer. Bytes buffered by
// the request writer after an early answer cannot change a finished turn.
type physicalOriginBody struct {
	io.ReadCloser
	upload *physicalH2UploadConn
}

func (*physicalOriginBody) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func (b *physicalOriginBody) Read(payload []byte) (int, error) {
	count, err := b.ReadCloser.Read(payload)
	if err != nil {
		_ = b.upload.Close()
	}
	return count, err //nolint:wrapcheck // retain the original decoded response read result
}

func (b *physicalOriginBody) Close() error {
	_ = b.upload.Close()
	return b.ReadCloser.Close() //nolint:wrapcheck // retain the original body close result after the write join
}

// Retained for the sealed proving fixture; production uses the same method.
func (t requestHTTPTransport) roundTripPhysicalOriginH2Prototype(request *http.Request) (*http.Response, error) {
	return t.roundTripPhysicalOrigin(request)
}
