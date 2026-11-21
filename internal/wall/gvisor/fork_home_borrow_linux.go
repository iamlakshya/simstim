//go:build linux

package gvisor

import (
	"context"
	"errors"
	"os"
)

// WithPreparedForkHomeV1 borrows only this prepared factory's fresh home for
// owned-volume accounting. It grants no running source or execution authority.
// The original lifecycle mutex joins this callback before home removal.
func (s *sandbox) WithPreparedForkHomeV1(ctx context.Context, use func(*os.Root) error) error {
	if s == nil || ctx == nil || use == nil {
		return failure("wall_setup", "the fresh-home accounting has no original factory", nil)
	}
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	var home *freshForkHomeV1
	if s.storage != nil {
		home = s.storage.home
	}
	valid := s.storage != nil && !s.released && !s.destroyed && !s.started
	s.mu.Unlock()
	if !valid || home == nil {
		return failure("wall_setup", "the original prepared fresh-home role ended", nil)
	}
	if err := errors.Join(ctx.Err(), home.check()); err != nil {
		return err
	}
	if err := use(home.root); err != nil {
		return err
	}
	return errors.Join(ctx.Err(), home.check())
}
