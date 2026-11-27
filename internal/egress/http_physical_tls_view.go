package egress

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/http2"
)

// Public NewClientConn receives a connection-owned plaintext view of the
// already verified native TLS connection. It cannot dial, retry or cache a
// second peer. TLS state is restored on the response by the original owner.
func physicalClientView(template *http.Transport, actual *tls.Conn, observer *physicalH2UploadConn, address, scheme string, check func(context.Context) bool) *http.Transport {
	view := template.Clone()
	protocols := new(http.Protocols)
	if actual != nil && actual.ConnectionState().NegotiatedProtocol == http2.NextProtoTLS {
		protocols.SetUnencryptedHTTP2(true)
	} else {
		protocols.SetHTTP1(true)
	}
	view.Protocols, view.TLSNextProto, view.Proxy = protocols, nil, nil
	view.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, errPhysicalH2Binding }
	var mu sync.Mutex
	used := false
	dial := func(ctx context.Context, network, requested string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if used || ctx.Err() != nil || network != "tcp" || requested != address || check == nil || !check(ctx) {
			return nil, errPhysicalH2Binding
		}
		used = true
		return observer, nil
	}
	if scheme == "https" {
		view.DialTLSContext = dial
	} else {
		view.DialContext = dial
		view.DialTLSContext = func(context.Context, string, string) (net.Conn, error) { return nil, errPhysicalH2Binding }
	}
	return view
}

// The same original transport supplies TLS trust and timeout settings. No
// request bytes can leave while the observer still assumes inspected size.
func physicalOriginUpload(request *http.Request, template *http.Transport, raw net.Conn, turn *ModelUsageTurnV1, host string) (*physicalH2UploadConn, *tls.Conn, error) {
	if request.URL.Scheme == "http" {
		observer, err := newPhysicalHTTP1UploadConn(turn, raw, true)
		return observer, nil, err
	}
	if request.URL.Scheme != "https" || template.TLSClientConfig == nil {
		return nil, nil, errPhysicalH2Binding
	}
	config := template.TLSClientConfig.Clone()
	config.ServerName = host
	// Respect an explicitly configured HTTP1-only template (the fixed Judge
	// path), while the ordinary origin transport retains negotiated HTTP2.
	if template.Protocols != nil && template.Protocols.HTTP2() {
		config.NextProtos = []string{http2.NextProtoTLS, "http/1.1"}
	}
	actual := tls.Client(raw, config)
	ctx, cancel := request.Context(), func() {}
	if timeout := template.TLSHandshakeTimeout; timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	err := actual.HandshakeContext(ctx)
	cancel()
	if err != nil {
		return nil, nil, err
	}
	observer, err := newPhysicalTLSUploadConn(turn, actual, host, true)
	return observer, actual, err
}
