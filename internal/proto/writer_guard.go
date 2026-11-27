package proto

import (
	"context"
	"encoding/json"
	"fmt"
)

// WriteJSONGuardedCtx encodes once and checks beforePut exactly once after
// acquiring the actual writer turn. A refusal writes no bytes. The callback
// grants nothing by itself; the caller retains its original source ownership
// through this method's return, including a failed physical write.
func (w *Writer) WriteJSONGuardedCtx(ctx context.Context, t Type, value any, beforePut func() error) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("proto: encode a frame: %w", err)
	}
	return w.writeCtxGuarded(ctx, t, payload, beforePut)
}

func (w *Writer) writeCtxGuarded(ctx context.Context, t Type, payload []byte, beforePut func() error) error {
	buf, err := frame(t, payload)
	if err != nil {
		return err
	}
	select {
	case w.turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.turn }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if beforePut != nil {
		if err := beforePut(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return w.put(buf)
}
