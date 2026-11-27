package egress

import (
	"errors"
	"io"
	"net/http"
	"sync"
)

var errSecretTrailerMisuse = errors.New("request trailer failed its credential check")

// Decoder Close never runs before the actual owned origin socket closes.
// This close does not join the handler or cancel another local TLS stream.
type secretOriginInput struct{ *secretOriginInputState }

type secretOriginInputState struct {
	reader io.ReadCloser
	scope  *originRequest
	once   sync.Once
	err    error
}

func (r *secretOriginInput) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *secretOriginInput) Close() error {
	r.once.Do(func() {
		r.scope.mu.Lock()
		conn := r.scope.conn
		r.scope.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		r.err = r.reader.Close()
	})
	return r.err
}

type secretResponseBody struct{ *secretResponseBodyState }

type secretResponseBodyState struct {
	reader   io.ReadCloser
	response *http.Response
	secrets  *boundHTTPSecrets
	finished bool
}

func (b *secretResponseBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) && !b.finished {
		b.finished = true
		// The inner policy reader has checked actual declarations/framing.
		// Populate only the safe copy before ReverseProxy emits its trailers.
		safe, failure := b.secrets.headers(b.response.Trailer, true)
		if failure != nil {
			return n, errResponseRegistry
		}
		b.response.Trailer = safe
	}
	return n, err
}

func (b *secretResponseBody) Close() error { return b.reader.Close() }

type secretRequestTrailerBody struct {
	reader  io.ReadCloser
	request *http.Request
	secrets *boundHTTPSecrets
}

func (b *secretRequestTrailerBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) {
		view, failure := b.secrets.s.requestSecretStreamSnapshot(b.secrets.scope.ctx)
		if failure != nil {
			return n, errResponseRegistry
		}
		if refusal := view.checkRequestSecretTrailers(b.request.Trailer, b.secrets.authority); refusal != nil {
			if refusal.decoy != "" {
				name := refusal.decoy
				b.secrets.decoy.Store(&name)
			}
			b.secrets.misuse.Store(true)
			return n, errSecretTrailerMisuse
		}
	}
	return n, err
}

func (b *secretRequestTrailerBody) Close() error { return b.reader.Close() }
