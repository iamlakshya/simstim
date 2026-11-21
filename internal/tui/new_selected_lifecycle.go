package tui

import (
	"context"
	"errors"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// This UI owner keeps the concrete client plan across questions. The client
// owns all native connections, request cleanup and retry classification.
type newSelectedStart struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	busy      chan struct{}
	selection *client.HostSelectionV1
	plan      *client.HostAttemptPlanV1
	attempt   *client.HostAttemptV1
	selector  string
	shown     hostSelectionPreview
	original  startReq
	options   client.NewInputOptions
	screen    screen
	current   string
	closeJob  *selectedNewCloseJob
}

type selectedNewCloseJob struct {
	done chan struct{}
	err  error
}

func (s *newSelectedStart) currentAttempt() *client.HostAttemptV1 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempt
}

func (s *newSelectedStart) setAttempt(attempt *client.HostAttemptV1) {
	s.mu.Lock()
	s.attempt = attempt
	s.mu.Unlock()
}

func (s *newSelectedStart) begin() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if s.busy != nil {
		return nil, changedNewSelection()
	}
	s.busy = make(chan struct{})
	done := s.busy
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.busy = nil
			close(done)
			s.mu.Unlock()
		})
	}, nil
}

func (s *newSelectedStart) close() error {
	if s == nil {
		return nil
	}
	s.cancel()
	s.mu.Lock()
	job := s.closeJob
	if job != nil {
		select {
		case <-job.done:
			if job.err != nil {
				job = nil // retry the same retained owners, never start new work
			}
		default:
		}
	}
	if job == nil {
		job = &selectedNewCloseJob{done: make(chan struct{})}
		s.closeJob = job
		busy := s.busy
		go func() {
			if busy != nil {
				<-busy
			}
			s.mu.Lock()
			plan, selection := s.plan, s.selection
			s.mu.Unlock()
			if plan != nil {
				job.err = plan.Close()
			} else if selection != nil {
				job.err = selection.Close()
			}
			close(job.done)
		}()
	}
	s.mu.Unlock()
	deadline := time.NewTimer(200 * time.Millisecond)
	defer deadline.Stop()
	select {
	case <-job.done:
		return job.err
	case <-deadline.C:
		return &client.UserError{Code: "host_attempt_cleanup", Kind: client.KindFailed,
			Msg: "the original host attempt is still closing", Hint: "Wait for its retained cleanup to finish before starting again."}
	}
}

type selectedNewOwners struct {
	mu   sync.Mutex
	held map[*newSelectedStart]error
}

func (o *selectedNewOwners) add(s *newSelectedStart) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.held == nil {
		o.held = map[*newSelectedStart]error{}
	}
	o.held[s] = nil
}

func (o *selectedNewOwners) release(s *newSelectedStart) error {
	if s == nil {
		return nil
	}
	err := s.close()
	o.mu.Lock()
	defer o.mu.Unlock()
	if err == nil {
		delete(o.held, s)
	} else {
		o.held[s] = err // keep uncertain native cleanup available for the next join
	}
	return err
}

func (o *selectedNewOwners) releaseAll() {
	o.mu.Lock()
	owners := make([]*newSelectedStart, 0, len(o.held))
	for owner := range o.held {
		owners = append(owners, owner)
	}
	o.mu.Unlock()
	for _, owner := range owners {
		_ = o.release(owner)
	}
}

func (o *selectedNewOwners) pendingError() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	var err error
	for _, pending := range o.held {
		err = errors.Join(err, pending)
	}
	return err
}

func (m *Model) selectedNewLive(s *newSelectedStart) bool {
	if s == nil || m.selectedNew != s || s.ctx.Err() != nil || m.screen != s.screen || m.cur != s.current {
		return false
	}
	switch current := questionOwner(m.modal).(type) {
	case *newSession:
		return current == s.original.form && current.starting
	case *makeFolderAsk:
		return current.selected == s
	case *setupAsk:
		return current.selected == s
	case *setupSkipAsk:
		return current.selected == s
	case *downgradeSheet:
		return current.request.selected == s
	}
	return false
}

func (m *Model) releaseSelectedNew(s *newSelectedStart) tea.Cmd {
	if s == nil {
		return nil
	}
	s.cancel()
	if m.selectedNew == s {
		m.selectedNew = nil
	}
	return m.bgCmd(func(context.Context) tea.Msg { return grantCleanupMsg{err: m.selectedOwners.release(s)} })
}

func (m *Model) retireSelectedNew() tea.Cmd {
	if owner := m.selectedNew; owner != nil && !m.selectedNewLive(owner) {
		return m.releaseSelectedNew(owner)
	}
	return nil
}

func (m *Model) startSelectedAgain(r startReq) tea.Cmd {
	owner := r.selected
	if owner == nil || !m.selectedNewLive(owner) || owner.currentAttempt() == nil {
		return m.newStartSourceError(r, changedNewSelection())
	}
	attempt := owner.currentAttempt()
	actual := attempt.Host()
	host := &hostConn{host: actual, conn: cleanConn{attempt.Conn()}, status: stOnline}
	return m.runSelectedNew(owner, m.startSessionWithSource(host, r))
}
