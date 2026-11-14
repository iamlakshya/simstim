package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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
	return c.callWithSent(ctx, op, args, out, nil)
}

// callWithSent releases an operation immediately after the frame write.
func (c *Conn) callWithSent(ctx context.Context, op string, args any, out any, afterWrite func()) error {
	var once sync.Once
	release := func() {
		once.Do(func() {
			if afterWrite != nil {
				afterWrite()
			}
		})
	}
	defer release()
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
	release()
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

// drop forgets a request that will get no answer.
func (c *Conn) drop(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}
