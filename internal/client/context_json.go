package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// This is the canonical selected/full JSON decoder over already owned bytes.
// It is not the FD metadata-only inspection and creates no source authority.
type contextJSONReader struct {
	ctx context.Context
	in  *bytes.Reader
}

func (r contextJSONReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.in.Read(data)
	if stopped := r.ctx.Err(); stopped != nil {
		return n, stopped
	}
	return n, err
}
func decodeContextJSON(ctx context.Context, raw []byte, value any) error {
	decoder := json.NewDecoder(contextJSONReader{ctx: ctx, in: bytes.NewReader(raw)})
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("JSON metadata contains trailing content")
	}
	return ctx.Err()
}
