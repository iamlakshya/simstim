package supervisor

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/phaseclock"
)

type runLimitTimer struct {
	fire           func()
	stopped, fired bool
}

func (t *runLimitTimer) Stop() bool {
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

type runLimitClock struct {
	at    time.Time
	delay time.Duration
	timer *runLimitTimer
}

func (c *runLimitClock) Now() time.Time { return c.at }
func (c *runLimitClock) AfterFunc(d time.Duration, f func()) phaseclock.Timer {
	c.delay = d
	c.timer = &runLimitTimer{}
	c.timer.fire = func() { c.timer.fired = true; f() }
	return c.timer
}

func runLimitFixture(t *testing.T, text string) (*Supervisor, *runLimitClock) {
	t.Helper()
	c := &runLimitClock{at: time.Unix(1700000000, 0)}
	previous := phaseclock.Current
	phaseclock.Current = c
	t.Cleanup(func() { phaseclock.Current = previous })
	s := &Supervisor{generation: "current-run", m: &host.Manifest{Policy: &api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{MaxRun: &text}}}}}
	s.rec.phase, s.rec.started = host.LifeRunning, c.at.Add(-5*time.Second)
	t.Cleanup(s.stopMaxRun)
	return s, c
}

func TestMaxRunUsesRecordedStartAndKeepsItsPolicySnapshot(t *testing.T) {
	s, c := runLimitFixture(t, "1m")
	if err := s.startMaxRun(); err != nil {
		t.Fatal(err)
	}
	if c.delay != 55*time.Second {
		t.Fatalf("timer delay=%s", c.delay)
	}
	*s.m.Policy.Sandbox.MaxRun = "4h"
	c.timer.fire()
	select {
	case <-s.maxRunDone():
	default:
		t.Fatal("recorded one-minute limit did not expire")
	}
}

func TestMaxRunCannotExpireAnotherGenerationOrStoppedRun(t *testing.T) {
	for _, change := range []string{"generation", "phase", "start"} {
		t.Run(change, func(t *testing.T) {
			s, c := runLimitFixture(t, "1m")
			if err := s.startMaxRun(); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "generation":
				s.generation = "replacement"
			case "phase":
				s.rec.phase = host.LifeEnding
			case "start":
				s.rec.started = c.at
			}
			c.timer.fire()
			select {
			case <-s.maxRunDone():
				t.Fatal("stale callback expired another run")
			default:
			}
		})
	}
}

func TestMaxRunStopRevokesAndJoinsTimer(t *testing.T) {
	s, c := runLimitFixture(t, "1m")
	if err := s.startMaxRun(); err != nil {
		t.Fatal(err)
	}
	s.stopMaxRun()
	if !c.timer.stopped || s.maxRunDone() != nil {
		t.Fatal("timer was retained after resource cleanup")
	}
	s.stopMaxRun()
}

func TestMaxRunUnsetDoesNotScheduleAndInvalidRefuses(t *testing.T) {
	for _, text := range []string{"", "bad", "0s", "-1s"} {
		t.Run(text, func(t *testing.T) {
			s, c := runLimitFixture(t, text)
			err := s.startMaxRun()
			if (text != "") != (err != nil) || c.timer != nil {
				t.Fatalf("limit %q: err=%v timer=%v", text, err, c.timer)
			}
		})
	}
}
