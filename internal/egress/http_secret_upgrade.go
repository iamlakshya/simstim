package egress

import (
	"context"
	"errors"
	"io"
	"net/http"
)

func (b *boundHTTPSecrets) prepareResponse(req *http.Request, response *http.Response) error {
	if b == nil || !b.active {
		return nil
	}
	if response.StatusCode == http.StatusSwitchingProtocols {
		if !b.webSocket {
			return errors.New("bound service selected an uninspectable upgrade")
		}
		if err := validateBoundWebSocketResponse(req, response); err != nil {
			return err
		}
	}
	for key := range response.Trailer {
		checked, err := b.scalar(key)
		if err != nil || checked != key {
			return errResponseRegistry
		}
	}
	headers, err := b.headers(response.Header, false)
	if err != nil {
		return err
	}
	response.Header = headers
	if response.StatusCode == http.StatusSwitchingProtocols {
		origin, ok := response.Body.(io.ReadWriteCloser)
		if !ok {
			return errors.New("bound WebSocket has no owned bidirectional origin")
		}
		filtered, err := newBoundWebSocket(b.scope.ctx, origin, webSocketFilters{
			Server: func(ctx context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
				return b.reader(ctx, input)
			},
			Client: func(ctx context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
				reader, err := newRequestSecretStreamReader(ctx, input, b.s.requestSecretStreamSnapshot)
				if err != nil {
					return nil, err
				}
				return &secretWebSocketClient{reader: reader, secrets: b}, nil
			},
		})
		if err != nil {
			return err
		}
		response.Body = filtered
		return nil
	}
	if response.Body == nil || response.Body == http.NoBody || req.Method == http.MethodHead {
		return nil
	}
	input := &secretOriginInput{secretOriginInputState: &secretOriginInputState{reader: response.Body, scope: b.scope}}
	reader, err := b.reader(b.scope.ctx, input)
	if err != nil {
		return err
	}
	response.Body = &secretResponseBody{secretResponseBodyState: &secretResponseBodyState{reader: reader, response: response, secrets: b}}
	return nil
}

type secretWebSocketClient struct {
	reader  io.ReadCloser
	secrets *boundHTTPSecrets
}

func (r *secretWebSocketClient) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if errors.Is(err, errRequestSecretStreamMisuse) {
		r.secrets.misuse.Store(true)
	}
	return n, err
}
func (r *secretWebSocketClient) Close() error { return r.reader.Close() }
