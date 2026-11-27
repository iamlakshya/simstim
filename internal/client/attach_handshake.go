package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// handshake sends the attach request and waits for the answer. reconnect is
// true when the attach resumes after a dropped link.
func (a *attacher) handshake(ctx context.Context, last int64, cols, rows, xpx, ypx int, reconnect bool) (proto.Attached, error) {
	trace, cats := a.conn.startTrace()
	req := proto.Attach{Session: a.o.Session, Cols: cols, Rows: rows, XPx: xpx, YPx: ypx, Client: brand.Name, LastOffset: last,
		Trace: trace, Categories: cats, Reconnect: reconnect}
	if err := a.conn.sendAttach(ctx, req); err != nil {
		return proto.Attached{}, err
	}
	timeout := time.After(15 * time.Second)
	for {
		select {
		case f := <-a.conn.Stream():
			switch f.Type {
			case proto.TAttached:
				var att proto.Attached
				if err := json.Unmarshal(f.Payload, &att); err != nil {
					return att, fmt.Errorf("read the host's answer to attach: %w", err)
				}
				if a.agent != nil && att.Session != a.o.Session {
					return proto.Attached{}, agentChannelError("the host acknowledged a different session")
				}
				return att, nil
			case proto.TError:
				var pe proto.Error
				_ = json.Unmarshal(f.Payload, &pe)
				return proto.Attached{}, hostError(&pe)
			default:
				// Other frames before the answer are ignored.
			}
		case <-a.conn.Done():
			return proto.Attached{}, ErrClosed
		case <-ctx.Done():
			return proto.Attached{}, ctx.Err()
		case <-timeout:
			return proto.Attached{}, errors.New("the host did not answer the attach in time")
		}
	}
}
