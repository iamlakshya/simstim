package egress

import (
	"context"
	"errors"
	"io"
	"net/http"
)

func (b *boundHTTPSecrets) prepareResponse(req *http.Request, response *http.Response, scope *originRequest, model *modelRequest) error {
	active := b != nil && b.active
	if !active && model == nil {
		return nil
	}
	if response.StatusCode == http.StatusSwitchingProtocols {
		if active && !b.webSocket || model != nil && !model.webSocket {
			return errors.New("service selected an uninspectable upgrade")
		}
		if err := validateBoundWebSocketResponse(req, response); err != nil {
			return err
		}
	}
	if active {
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
	}
	if response.StatusCode == http.StatusSwitchingProtocols {
		origin, ok := response.Body.(io.ReadWriteCloser)
		if !ok {
			return errors.New("WebSocket has no owned bidirectional origin")
		}
		filters := webSocketFilters{Server: identityWebSocketFilter, Client: identityWebSocketFilter}
		if active {
			filters.Server = func(ctx context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
				return b.reader(ctx, input)
			}
			filters.Client = func(ctx context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
				reader, err := newRequestSecretStreamReader(ctx, input, b.s.requestSecretStreamSnapshot)
				if err != nil {
					return nil, err
				}
				return &secretWebSocketClient{reader: reader, secrets: b}, nil
			}
		}
		if model != nil {
			credential := filters.Client
			filters.Client = func(ctx context.Context, opcode byte, input io.ReadCloser) (io.ReadCloser, error) {
				checked, err := credential(ctx, opcode, input)
				if err != nil {
					return nil, err
				}
				return scope.s.inspectModelWebSocket(ctx, scope, model, opcode, checked)
			}
		}
		filtered, err := newBoundWebSocket(scope.ctx, origin, filters)
		if err != nil {
			return err
		}
		response.Body = filtered
		return nil
	}
	if !active {
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
		var decoy *decoyStreamUse
		if errors.As(err, &decoy) {
			name := decoy.name
			r.secrets.decoy.Store(&name)
		}
		r.secrets.misuse.Store(true)
	}
	return n, err
}
func (r *secretWebSocketClient) Close() error { return r.reader.Close() }
