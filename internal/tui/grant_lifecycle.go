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
	mu   sync.Mutex
	held map[*client.GrantRequest]error
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

func (g *grantOwners) release(request *client.GrantRequest) error {
	if request == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.held[request]; !exists {
		return nil
	}
	err := request.Close()
	if err == nil {
		delete(g.held, request)
	} else {
		g.held[request] = err
	}
	return err
}

func (g *grantOwners) releaseAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for request := range g.held {
		if err := request.Close(); err == nil {
			delete(g.held, request)
		} else {
			g.held[request] = err
		}
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
	return m.bgCmd(func(context.Context) tea.Msg { return grantCleanupMsg{err: m.grantOwners.release(request)} })
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

func (m *Model) finishStartGrant(message tea.Msg) tea.Msg {
	result, ok := message.(startedMsg)
	if !ok || keepStartGrant(result) {
		return message
	}
	result.cleanupErr = m.grantOwners.release(result.grants)
	return result
}
