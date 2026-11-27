package inputtransfer

import (
	"bufio"
	"context"
)

// completion owns the sole put-output reader for ready, staged, complete and
// EOF. Reading it during metadata and payload writes lets an early failure
// cancel blocked writes. There is no unbounded event queue.
type completion struct {
	ready chan struct{}
	done  chan struct{}
	err   error
	peer  bool
}

func readCompletion(ctx context.Context, cancel context.CancelFunc, reader *bufio.Reader, scope Scope, count int, payloadEnded <-chan struct{},
	forward func(string) error, failure func(error)) *completion {
	result := &completion{ready: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(result.done)
		_, result.err = expectEvent(ctx, reader, scope, "input.ready", 0)
		if result.err == nil {
			close(result.ready)
		}
		for _, phase := range []string{"input.staged", "input.complete"} {
			if result.err != nil {
				break
			}
			_, result.err = expectEvent(ctx, reader, scope, phase, count)
			if result.err != nil {
				break
			}
			select {
			case <-payloadEnded:
			default:
				result.err = protocol("input completion preceded payload EOF admission")
			}
			if result.err != nil {
				break
			}
			if forward != nil {
				result.err = forward(phase)
				if result.err != nil {
					break
				}
			}
		}
		if result.err == nil {
			if _, err := readFrame(ctx, reader); !isEOF(err) {
				result.err = protocol("input completion was not followed by exact stream EOF")
			}
		}
		if result.err != nil && ctx.Err() == nil {
			result.peer = true
			if failure != nil {
				failure(result.err)
			}
			cancel()
		}
	}()
	return result
}

func (c *completion) waitReady(ctx context.Context) error {
	select {
	case <-c.ready:
		return nil
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *completion) join(caller context.Context, current error) error {
	if c == nil {
		return current
	}
	<-c.done
	if caller.Err() != nil {
		return caller.Err()
	}
	if c.peer || current == nil {
		return c.err
	}
	return current
}
