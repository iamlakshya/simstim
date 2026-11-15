package supervisor

import (
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/phaseclock"
)

// maxRunTimer belongs to one recorded primary start. Policy edits and guest
// claims cannot move its deadline, and an old callback cannot stop a new run.
type maxRunTimer struct {
	timer      phaseclock.Timer
	expired    chan struct{}
	joined     chan struct{}
	generation string
	started    time.Time
}

// startMaxRun runs after the running record's durable save, before wait.
func (s *Supervisor) startMaxRun() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxRun != nil {
		return wallError("the sandbox already has a run timer", nil)
	}
	if s.m.Policy == nil || s.m.Policy.Sandbox.MaxRun == nil || *s.m.Policy.Sandbox.MaxRun == "" {
		return nil
	}
	limit, err := time.ParseDuration(*s.m.Policy.Sandbox.MaxRun)
	if err != nil || limit <= 0 {
		return wallError("sandbox.max_run must be a positive duration", err)
	}
	if s.primary != nil || s.generation == "" || s.rec.phase != host.LifeRunning || s.rec.started.IsZero() {
		return wallError("the run timer has no recorded primary start", nil)
	}
	watch := &maxRunTimer{expired: make(chan struct{}), joined: make(chan struct{}), generation: s.generation, started: s.rec.started}
	s.maxRun = watch
	watch.timer = phaseclock.Current.AfterFunc(max(time.Duration(0), watch.started.Add(limit).Sub(phaseclock.Current.Now())), func() {
		defer close(watch.joined)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.maxRun == watch && s.generation == watch.generation && s.rec.started.Equal(watch.started) && s.rec.phase == host.LifeRunning {
			close(watch.expired)
		}
	})
	return nil
}

// maxRunDone supplies a nil channel when this start has no limit.
func (s *Supervisor) maxRunDone() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxRun == nil {
		return nil
	}
	return s.maxRun.expired
}

// stopMaxRun revokes and joins the one callback before resource teardown.
func (s *Supervisor) stopMaxRun() {
	s.mu.Lock()
	watch := s.maxRun
	s.maxRun = nil
	s.mu.Unlock()
	if watch == nil {
		return
	}
	if watch.timer.Stop() {
		close(watch.joined)
	}
	<-watch.joined
}
