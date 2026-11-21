package client

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ownedDialStartV1 retains even a native Start which returns after cancellation,
// before a Conn exists. Each returned process has exactly one Wait owner.
type ownedDialStartV1 struct {
	mu       sync.Mutex
	started  chan struct{}
	reaped   chan struct{}
	reapOnce sync.Once
	proc     Proc
	startErr error
	waitErr  error
	handed   bool
	conn     *Conn
}

type ownedDialFailureV1 struct {
	start  *ownedDialStartV1
	actual error
}

func (e *ownedDialFailureV1) Error() string { return e.actual.Error() }
func (e *ownedDialFailureV1) Unwrap() error { return e.actual }

func beginOwnedNewDialStartV1(ctx context.Context) *ownedDialStartV1 {
	owner, _ := ctx.Value(ownedNewDialKeyV1{}).(*ownedNewDialV1)
	if owner == nil {
		return nil
	}
	start := &ownedDialStartV1{started: make(chan struct{}), reaped: make(chan struct{})}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.finished {
		owner.captureErr = grantOperationError()
		return nil
	}
	owner.starts = append(owner.starts, start)
	return start
}

func dialStartOwnedV1(ctx context.Context, transport Transport, remote string, start *ownedDialStartV1) (Proc, error) {
	if start == nil {
		return dialStart(ctx, transport, remote)
	}
	type result struct {
		proc Proc
		err  error
	}
	ready := make(chan result)
	go func() {
		proc, err := transport.Start(ctx, remote)
		start.mu.Lock()
		start.proc, start.startErr = proc, err
		start.mu.Unlock()
		close(start.started)
		select {
		case ready <- result{proc, err}:
		case <-ctx.Done():
			start.reapV1()
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ready:
		if err := ctx.Err(); err != nil {
			start.reapV1()
			return nil, err
		}
		if result.err != nil {
			start.reapV1()
			return result.proc, result.err
		}
		if result.proc == nil {
			start.reapV1()
			return nil, grantOperationError()
		}
		start.mu.Lock()
		start.handed = true
		start.mu.Unlock()
		return result.proc, nil
	}
}

func (s *ownedDialStartV1) reapV1() {
	s.reapOnce.Do(func() {
		go func() {
			// Start completed before this method was called. No Conn owns it.
			s.mu.Lock()
			proc := s.proc
			s.mu.Unlock()
			var err error
			if proc != nil {
				proc.Kill()
				if output, ok := proc.Stdout().(io.Closer); ok {
					_ = output.Close()
				}
				if input := proc.Stdin(); input != nil {
					_ = input.Close()
				}
				err = proc.Wait()
			}
			s.mu.Lock()
			s.waitErr = err
			s.mu.Unlock()
			close(s.reaped)
		}()
	})
}

func (o *ownedNewDialV1) retryableUnreachableV1() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	failure, exact := o.dialErr.(*ownedDialFailureV1)
	finished, captureErr := o.finished, o.captureErr
	starts := append([]*ownedDialStartV1(nil), o.starts...)
	o.mu.Unlock()
	if !finished || captureErr != nil || !exact || failure.start == nil ||
		len(starts) == 0 || starts[len(starts)-1] != failure.start ||
		errors.Is(failure.actual, context.Canceled) || errors.Is(failure.actual, context.DeadlineExceeded) {
		return false
	}
	// A public error code is only a refusal filter. The actual native result
	// must independently be unreachable and its sole waiter must have joined.
	var user *UserError
	if errors.As(failure.actual, &user) && (user.Code != "unreachable" || user.Kind == KindRefused) {
		return false
	}
	s := failure.start
	select {
	case <-s.started:
	default:
		return false
	}
	s.mu.Lock()
	conn, proc, startErr := s.conn, s.proc, s.startErr
	s.mu.Unlock()
	if conn != nil {
		select {
		case <-conn.procDone:
		default:
			return false
		}
		return errors.Is(conn.processWaitErrorV1(), ErrUnreachable)
	}
	select {
	case <-s.reaped:
	default:
		return false
	}
	// No process is a positive native Start result, never an empty capture.
	return proc == nil && errors.Is(startErr, ErrUnreachable)
}
