package client

import "context"

// dialStart keeps ownership of every started process. A transport can finish
// starting after cancellation, so it hands its result over only while dial
// is still waiting. Otherwise this goroutine stops and reaps the late result.
func dialStart(ctx context.Context, transport Transport, remote string) (Proc, error) {
	type result struct {
		proc Proc
		err  error
	}
	ready := make(chan result)
	go func() {
		proc, err := transport.Start(ctx, remote)
		select {
		case ready <- result{proc, err}:
		case <-ctx.Done():
			reapDialStart(proc)
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ready:
		if err := ctx.Err(); err != nil {
			go reapDialStart(r.proc)
			return nil, err
		}
		if r.err != nil {
			go reapDialStart(r.proc)
		}
		return r.proc, r.err
	}
}

func reapDialStart(proc Proc) {
	if proc != nil {
		proc.Kill()
		_ = proc.Wait()
	}
}
