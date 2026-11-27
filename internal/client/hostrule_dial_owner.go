package client

import (
	"context"
	"errors"
	"io"
	"sync"
)

type ownedNewDialKeyV1 struct{}

// ownedNewDialV1 keeps every actual connection allocated by one Dial, including
// failed hello/install retries. A code, closed pipe or timer creates none of
// these owners and can never prove their native process has exited.
type ownedNewDialV1 struct {
	mu         sync.Mutex
	conns      []*Conn
	starts     []*ownedDialStartV1
	main       *Conn
	dialErr    error
	waitErrs   []error
	captureErr error
	finished   bool
}

func dialOwnedNewAttemptV1(ctx context.Context, host Host, interactive bool) (*ownedNewDialV1, error) {
	if ctx == nil || ctx.Value(ownedNewDialKeyV1{}) != nil {
		return nil, grantOperationError()
	}
	owner := &ownedNewDialV1{}
	owned := context.WithValue(ctx, ownedNewDialKeyV1{}, owner)
	conn, err := Dial(owned, host, interactive)
	owner.mu.Lock()
	owner.main, owner.dialErr, owner.finished = conn, err, true
	if conn != nil {
		found := false
		for _, actual := range owner.conns {
			found = found || actual == conn
		}
		if !found {
			owner.captureErr = grantOperationError()
		}
	}
	captureErr := owner.captureErr
	owner.mu.Unlock()
	return owner, errors.Join(err, captureErr)
}

// dialObserved calls this immediately after creating the actual procDone and
// before the first hello. It observes actual allocation, never a caller DTO.
func recordOwnedNewDialConnectionV1(ctx context.Context, conn *Conn, start *ownedDialStartV1) {
	owner, _ := ctx.Value(ownedNewDialKeyV1{}).(*ownedNewDialV1)
	if owner == nil {
		return
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.finished || conn == nil || conn.proc == nil || conn.procDone == nil {
		owner.captureErr = grantOperationError()
		return
	}
	for _, before := range owner.conns {
		if before == conn {
			return
		}
	}
	if start == nil {
		owner.captureErr = grantOperationError()
		return
	}
	start.mu.Lock()
	if start.conn != nil || !start.handed {
		owner.captureErr = grantOperationError()
		start.mu.Unlock()
		return
	}
	start.conn = conn
	start.mu.Unlock()
	owner.conns = append(owner.conns, conn)
}

func (o *ownedNewDialV1) connection() *Conn {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.main
}

// settle closes only recorded owners, preserving keep while it serves New.
// It reports success only after each retired owner's one actual Wait joins.
// An expired cleanup context retains the complete owner for a later join.
func (o *ownedNewDialV1) settle(ctx context.Context, keep *Conn) (bool, error) {
	if o == nil || ctx == nil {
		return false, grantOperationError()
	}
	o.mu.Lock()
	if !o.finished {
		o.mu.Unlock()
		return false, grantOperationError()
	}
	conns := append([]*Conn(nil), o.conns...)
	starts := append([]*ownedDialStartV1(nil), o.starts...)
	main := o.main
	captureErr := o.captureErr
	o.mu.Unlock()
	if keep != nil && keep != main {
		return false, grantOperationError()
	}
	var waitErrs []error
	for _, start := range starts {
		select {
		case <-start.started:
		default:
			select {
			case <-start.started:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		start.mu.Lock()
		conn, handed := start.conn, start.handed
		start.mu.Unlock()
		if conn != nil {
			continue
		}
		if handed {
			return false, grantOperationError()
		}
		select {
		case <-start.reaped:
		default:
			select {
			case <-start.reaped:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		start.mu.Lock()
		if start.waitErr != nil {
			waitErrs = append(waitErrs, start.waitErr)
		}
		start.mu.Unlock()
	}
	for _, conn := range conns {
		if conn == keep {
			continue
		}
		select {
		case <-conn.procDone:
			if err := conn.processWaitErrorV1(); err != nil {
				waitErrs = append(waitErrs, err)
			}
			continue
		default:
		}
		conn.killStartup()
		if output, ok := conn.proc.Stdout().(io.Closer); ok {
			_ = output.Close()
		}
		if conn.stdin != nil {
			_ = conn.stdin.Close()
		}
		conn.fail(ErrClosed)
		select {
		case <-conn.procDone:
			if err := conn.processWaitErrorV1(); err != nil {
				waitErrs = append(waitErrs, err)
			}
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	o.mu.Lock()
	o.waitErrs = waitErrs
	o.mu.Unlock()
	if captureErr != nil {
		return false, captureErr
	}
	return true, nil
}

func (o *ownedNewDialV1) waitError() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return errors.Join(o.waitErrs...)
}
