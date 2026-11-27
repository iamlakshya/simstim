//go:build linux || darwin

package none

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/wall"
)

// LiveOwnerV1 is borrowed evidence from this exact created init and Start
// connection. No saved PID or path constructs it; its completed cleanup
// exposes diagnostic observations only.
type LiveOwnerV1 struct {
	runtime    *sandbox
	connection net.Conn
	birth      string
}

func NativeLiveOwnerV1(runtime wall.Sandbox, connection net.Conn) (*LiveOwnerV1, error) {
	s, ok := runtime.(*sandbox)
	if !ok || connection == nil {
		return nil, failure("the unwalled source has no original runtime and connection", nil)
	}
	s.mu.Lock()
	if !s.started || !s.handed || s.closed || s.cmd == nil || s.connection != connection || s.root == nil {
		s.mu.Unlock()
		return nil, failure("the unwalled source has no same actual Start-returned connection", nil)
	}
	owner := s.live
	if owner == nil {
		if err := checkUnwalledHandle(s.pidfd); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		birth, err := unwalledBirthV1(s.cmd.Process.Pid)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		if err := checkUnwalledHandle(s.pidfd); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		owner = &LiveOwnerV1{runtime: s, connection: connection, birth: birth}
		s.live = owner
	}
	s.mu.Unlock()
	if err := owner.Check(context.Background()); err != nil {
		return nil, err
	}
	return owner, nil
}
func (owner *LiveOwnerV1) Check(ctx context.Context) error {
	if owner == nil || owner.runtime == nil || ctx == nil {
		return failure("the original unwalled runtime owner is absent", nil)
	}
	s := owner.runtime
	for !s.life.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer s.life.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || !s.handed || s.closed || s.cmd == nil || s.connection != owner.connection || s.live != owner || s.root == nil {
		return failure("the original unwalled runtime ended or changed", nil)
	}
	select {
	case <-s.observed:
		return failure("the original unwalled init has physically ended", nil)
	default:
	}
	return errors.Join(ctx.Err(), s.rootRef.CheckRoot(s.root), checkUnwalledHandle(s.pidfd))
}

func (owner *LiveOwnerV1) CheckSessionV1(ctx context.Context, id string, connection net.Conn) error {
	if owner == nil || owner.runtime == nil || connection == nil || connection != owner.connection || owner.runtime.spec.ID != id {
		return failure("the original unwalled creator has another session or Start connection", nil)
	}
	return owner.Check(ctx)
}
