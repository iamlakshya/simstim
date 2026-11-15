package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/iamlakshya/simstim/internal/proto"
)

// requestStage observes the same queued/upload publication stages in tests.
var requestStage = func(string) {}

// Call uses the ONE ordered request writer and pending-response table.
func (c *Conn) Call(ctx context.Context, op string, args any, out any) error {
	return c.callWithSent(ctx, op, args, out, nil)
}

// callWithSent releases publication immediately after the actual frame write.
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
	id, ch, err := c.startCall(ctx, op, args)
	if err != nil {
		return err
	}
	release()
	return c.awaitCall(ctx, op, id, ch, out)
}

// startCall returns only AFTER the same physical ordered frame writer. Runtime
// aborts can therefore follow their fetch without racing request publication.
// New's private wire/ACK witness shares this path; there is no parallel RPC.
func (c *Conn) startCall(ctx context.Context, op string, args any) (uint64, chan proto.Response, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	id := c.nextID.Add(1)
	req := proto.Request{ID: id, Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return 0, nil, fmt.Errorf("encode the %s request: %w", op, err)
		}
		req.Args = b
	}
	witness, _ := ctx.Value(newPublicationKey{}).(*newPublicationWitness)
	if witness != nil {
		if err := witness.beforeRequest(ctx, c, req, args); err != nil {
			return 0, nil, err
		}
	}
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return 0, nil, err
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return 0, nil, err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	requestStage("queued")
	if plan, _ := ctx.Value(newPlanPublicationKey{}).(*newSecretPlan); plan != nil {
		if err := plan.markPublicationAttempt(); err != nil {
			c.drop(id)
			return 0, nil, err
		}
	}
	if err := c.w.WriteJSONCtx(ctx, proto.TRequest, req); err != nil {
		c.drop(id)
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, ErrClosed
	}
	if witness != nil {
		witness.wroteRequest(c, id)
	}
	return id, ch, nil
}

func (c *Conn) awaitCall(ctx context.Context, op string, id uint64, ch chan proto.Response, out any) error {
	return c.awaitCallResult(ctx, op, id, ch, out, false)
}

// Only explicit typed purge/report callers request partial result decoding.
// A refusal never becomes a successful publication or an ACK receipt.
func (c *Conn) awaitCallResult(ctx context.Context, op string, id uint64, ch chan proto.Response, out any, partial bool) error {
	select {
	case <-ctx.Done():
		c.drop(id)
		return ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if resp.ID != id {
			return &UserError{Msg: "the host answered a different request", Hint: "Reconnect to the host and retry the operation.", Code: "protocol", Kind: KindRefused}
		}
		if (resp.OK || partial) && out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("read the answer to %s: %w", op, err)
			}
		}
		if !resp.OK {
			if resp.Error != nil {
				return hostErrorOn(c.Host.Name, resp.Error)
			}
			return errors.New("the host refused the request")
		}
		if witness, _ := ctx.Value(newPublicationKey{}).(*newPublicationWitness); witness != nil {
			if err := witness.afterResponse(ctx, c, resp, out); err != nil {
				return err
			}
		}
		return nil
	}
}

func (c *Conn) drop(id uint64) { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }
