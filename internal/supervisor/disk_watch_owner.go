package supervisor

import (
	"context"
	"errors"
)

// diskWatcherV1 borrows only its actual factory capability. Stop joins the
// single watcher; the same runtime owner releases the capability afterwards.
type diskWatcherV1 struct {
	resources *diskResourcesV1
	cancel    context.CancelFunc
	done      chan struct{}
	err       error
}

func (s *Supervisor) prepareDiskWatcherV1(runtime sandbox, initial context.Context) error {
	if initial == nil {
		return diskFailure("disk_measure", "the initial disk check has no owned startup budget")
	}
	// The explicitly selected plain fixture has no real sandbox storage plan.
	if s.m.Wall != nil && s.m.Wall.Driver == driverPlain {
		return nil
	}
	source, known := runtime.(ownedDiskRuntimeV1)
	if !known {
		return diskFailure("disk_measure", "the actual runtime factory has no owned disk capability")
	}
	resources := source.ownedDiskResourcesV1()
	ctx, cancel := context.WithCancel(s.lifetimeContext())
	watcher := &diskWatcherV1{resources: resources, cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.diskWatcher = watcher
	generation := s.generation
	s.mu.Unlock()
	options, err := resources.options(func(reason error) { s.requestDiskStop(generation, reason) })
	if err == nil {
		err = checkDisk(initial, options)
	}
	if initial.Err() != nil {
		err = initial.Err()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		if ctx.Err() == nil && initial.Err() == nil {
			s.requestDiskStop(generation, err)
		}
		watcher.err = err
		cancel()
		close(watcher.done)
		return err
	}
	go func() { defer close(watcher.done); watcher.err = watchDisk(ctx, options) }()
	return nil
}
func (s *Supervisor) stopDiskWatcherV1() error {
	s.mu.Lock()
	watcher := s.diskWatcher
	s.mu.Unlock()
	if watcher == nil {
		return nil
	}
	watcher.cancel()
	<-watcher.done
	if errors.Is(watcher.err, context.Canceled) || errors.Is(watcher.err, context.DeadlineExceeded) {
		return nil
	}
	return watcher.err
}
func (s *Supervisor) closeDiskResourcesV1() error {
	s.mu.Lock()
	watcher := s.diskWatcher
	runtime := s.sb
	s.mu.Unlock()
	if watcher != nil {
		return watcher.resources.Close()
	}
	if source, known := runtime.(ownedDiskRuntimeV1); known {
		return source.ownedDiskResourcesV1().Close()
	}
	return nil
}
