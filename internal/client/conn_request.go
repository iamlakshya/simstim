package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/proto"
)

// requestStage is called at stages of a request: "queued", once Call has
// looked at ctx and is about to wait for its turn to write, and "upload
// begin", "upload chunk" and "upload end", before each frame of an upload,
// and "upload sent", once its frames are out. Tests cancel there.
var requestStage = func(string) {}

// Call sends a request and decodes the result into out. Every request to
// the host goes through it, and none goes after ctx has ended: what asked
// for it has stopped, so the host must not act on it. It then returns
// ctx's error, and writes nothing: also when ctx ends while the request
// waits behind another frame (proto.Writer.WriteCtx).
func (c *Conn) Call(ctx context.Context, op string, args any, out any) error {
	id := c.nextID.Add(1)
	req := proto.Request{ID: id, Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return fmt.Errorf("encode the %s request: %w", op, err)
		}
		req.Args = b
	}
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	requestStage("queued")
	if err := c.w.WriteJSONCtx(ctx, proto.TRequest, req); err != nil {
		c.drop(id)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrClosed
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if !resp.OK {
			if resp.Error != nil {
				return hostErrorOn(c.Host.Name, resp.Error)
			}
			return errors.New("the host refused the request")
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("read the answer to %s: %w", op, err)
			}
		}
		return nil
	}
}

// Stream returns attach frames.
func (c *Conn) Stream() <-chan proto.Frame { return c.stream }

// Send writes a raw frame. With the debug trace on, it records each key.
func (c *Conn) Send(t proto.Type, p []byte) error {
	if c.tr != nil && t == proto.TInput {
		return c.tr.sendKey(c.w, p)
	}
	return c.w.Write(t, p)
}

// SendJSON writes a JSON frame.
func (c *Conn) SendJSON(t proto.Type, v any) error { return c.w.WriteJSON(t, v) }

// uploadTurn is the turn of whole uploads on c: the host takes one upload
// at a time from a connection, and drops an unfinished one when the next
// begins (host's bridge_upload.go).
func (c *Conn) uploadTurn() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.uploading == nil {
		c.uploading = make(chan struct{}, 1)
	}
	return c.uploading
}

// drop forgets the request id, which will get no answer.
func (c *Conn) drop(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Upload sends a file into a session's paste folder and returns its path
// on the host. As Call, it sends nothing after ctx has ended. A cancel in
// the middle stops before the next frame: the host never gets the end of
// the upload, and reclaims it. Uploads on c take turns, whole: one waits
// for the one before it, until ctx ends.
func (c *Conn) Upload(ctx context.Context, session, name string, data []byte) (string, error) {
	turn := c.uploadTurn()
	select {
	case turn <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-turn }()
	id := c.nextID.Add(1)
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return "", c.err
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return "", err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	failed := func() (string, error) {
		c.drop(id)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrClosed
	}
	requestStage("upload begin")
	if err := c.w.WriteJSONCtx(ctx, proto.TUploadBegin, proto.UploadBegin{ID: id, Session: session, Name: name, Size: int64(len(data))}); err != nil {
		return failed()
	}
	for off := 0; off < len(data); off += 256 << 10 {
		end := off + 256<<10
		if end > len(data) {
			end = len(data)
		}
		requestStage("upload chunk")
		if err := c.w.WriteCtx(ctx, proto.TUploadChunk, proto.EncodeChunk(id, data[off:end])); err != nil {
			return failed()
		}
	}
	sum := sha256.Sum256(data)
	requestStage("upload end")
	if err := c.w.WriteJSONCtx(ctx, proto.TUploadEnd, proto.UploadEnd{ID: id, SHA256: hex.EncodeToString(sum[:])}); err != nil {
		return failed()
	}
	requestStage("upload sent")
	select {
	case <-ctx.Done():
		c.drop(id) // its answer, if one comes, goes nowhere
		return "", ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return "", ErrClosed
		}
		if !resp.OK {
			if resp.Error != nil {
				return "", hostError(resp.Error)
			}
			return "", errors.New("upload failed")
		}
		var r proto.UploadResult
		if err := json.Unmarshal(resp.Result, &r); err != nil {
			return "", fmt.Errorf("read the answer to an upload: %w", err)
		}
		return r.Path, nil
	}
}
