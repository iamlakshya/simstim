package tui

import (
	"context"
	"errors"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// grantOwners retains failed cleanup. A request may span setup questions,
// but every program pause joins its commands before releasing the leases.
type grantOwners struct {
	mu       sync.Mutex
	held     map[*client.GrantRequest]error
	attempts map[*client.GrantRequest]*grantAttempt
}

type grantAttempt struct {
	ctx                        context.Context
	cancel                     context.CancelFunc
	done                       chan struct{}
	active, terminal, cleaning bool
	finished                   chan struct{}
	err                        error
}

func (g *grantOwners) add(request *client.GrantRequest) {
	if request == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held == nil {
		g.held = map[*client.GrantRequest]error{}
	}
	if _, exists := g.held[request]; !exists {
		g.held[request] = nil
	}
}

// begin binds the actual bounded attempt and its join to the existing owner.
// Folder/setup retries retain R but never gain another attempt's deadline.
func (g *grantOwners) begin(request *client.GrantRequest, ctx context.Context, cancel context.CancelFunc) (func(), error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, &client.UserError{Msg: "the start has no owned attempt deadline", Hint: "Start the session again.", Code: "grant_scope", Kind: client.KindRefused}
	}
	g.add(request)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.attempts == nil {
		g.attempts = map[*client.GrantRequest]*grantAttempt{}
	}
	owner := g.attempts[request]
	if owner == nil {
		owner = &grantAttempt{}
		g.attempts[request] = owner
	}
	if owner.active || owner.terminal {
		return nil, &client.UserError{Msg: "the start's original attempt is active or closing", Hint: "Wait for its cleanup, then start a new session.", Code: "grant_scope", Kind: client.KindRefused}
	}
	owner.ctx, owner.cancel, owner.done, owner.active = ctx, cancel, make(chan struct{}), true
	done := owner.done
	var once sync.Once
	return func() { once.Do(func() { cancel(); g.mu.Lock(); owner.active = false; close(done); g.mu.Unlock() }) }, nil
}

func grantCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		// Preserve this attempt's ORIGINAL deadline after its work was cancelled.
		// A new cleanup grace must never extend its owner budget.
		return context.WithDeadline(context.WithoutCancel(ctx), deadline)
	}
	return ctx, func() {}
}

func (g *grantOwners) release(ctx context.Context, request *client.GrantRequest) error {
	if request == nil {
		return nil
	}
	g.mu.Lock()
	if _, exists := g.held[request]; !exists {
		g.mu.Unlock()
		return nil
	}
	owner := g.attempts[request]
	if owner == nil {
		owner = &grantAttempt{ctx: ctx}
		if g.attempts == nil {
			g.attempts = map[*client.GrantRequest]*grantAttempt{}
		}
		g.attempts[request] = owner
	}
	if owner.cleaning {
		finished := owner.finished
		g.mu.Unlock()
		<-finished
		g.mu.Lock()
		err := owner.err
		g.mu.Unlock()
		return err
	}
	owner.terminal, owner.cleaning, owner.finished = true, true, make(chan struct{})
	budget, cancel, done := owner.ctx, owner.cancel, owner.done
	if !owner.active {
		done = nil
	}
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	cleanup, finish := grantCleanupContext(budget)
	_, abortErr := request.Abort(cleanup)
	err := errors.Join(abortErr, request.Close())
	finish()
	g.mu.Lock()
	defer g.mu.Unlock()
	owner.err, owner.cleaning = err, false
	close(owner.finished)
	if err == nil {
		delete(g.held, request)
		delete(g.attempts, request)
	} else {
		g.held[request] = err
	}
	return err
}

func (g *grantOwners) releaseAll(ctx context.Context) {
	g.mu.Lock()
	requests := make([]*client.GrantRequest, 0, len(g.held))
	for request := range g.held {
		requests = append(requests, request)
	}
	g.mu.Unlock()
	for _, request := range requests {
		_ = g.release(ctx, request)
	}
}

func (g *grantOwners) pendingError() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var pending error
	for _, err := range g.held {
		pending = errors.Join(pending, err)
	}
	return pending
}

type grantCleanupMsg struct{ err error }

func (m *Model) releaseGrant(request *client.GrantRequest) tea.Cmd {
	if request == nil {
		return nil
	}
	return m.bgCmd(func(ctx context.Context) tea.Msg { return grantCleanupMsg{err: m.grantOwners.release(ctx, request)} })
}

func keepStartGrant(msg startedMsg) bool {
	if _, ok := client.FolderQuestionOf(msg.err); ok {
		return true
	}
	if _, ok := client.SetupQuestionOf(msg.err); ok {
		return true
	}
	_, ok := setupCannotRun(msg.err)
	return ok && msg.args.Setup != api.SetupSkip
}

func (m *Model) finishStartGrant(ctx context.Context, message tea.Msg) tea.Msg {
	result, ok := message.(startedMsg)
	if !ok || keepStartGrant(result) {
		return message
	}
	result.cleanupErr = m.grantOwners.release(ctx, result.grants)
	return result
}
