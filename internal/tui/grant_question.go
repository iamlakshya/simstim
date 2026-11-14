package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type grantQuestionRequest struct {
	ctx      context.Context
	question *client.ConfirmError
	answer   chan bool
	ended    chan struct{}
}

type grantQuestionEndedMsg struct{ answer chan bool }

type grantAnswer struct {
	yes bool
	err error
}

// grantQuestions keeps the original operation, source and request alive while
// Update owns a live answer. A confirmation never starts a new value request.
func (m *Model) grantQuestions(ctx context.Context, run func(context.Context) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.ctx.Err(); err != nil {
			return err
		}
		err := run(ctx)
		var question *client.ConfirmError
		if !errors.As(err, &question) {
			return err
		}
		if question == nil || question.Prompt == "" || !client.ValidHostName(question.Host) || question.Kind != client.ConfirmYesNo && question.Kind != client.ConfirmHostName {
			return &client.UserError{Msg: "the grant question is not valid", Hint: "Start the operation again.", Code: "grant_confirmation", Kind: client.KindRefused}
		}
		yes, waitErr := m.askGrant(ctx, question)
		if waitErr != nil {
			return waitErr
		}
		if !yes {
			return &client.UserError{Msg: "the grant was not approved", Hint: "Start the operation again if you want to approve it.", Code: "cancelled", Kind: client.KindCancelled}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.ctx.Err(); err != nil {
			return err
		}
		ctx, err = client.ConfirmGrant(ctx, err)
		if err != nil {
			return err
		}
	}
}

func (m *Model) askGrant(ctx context.Context, question *client.ConfirmError) (bool, error) {
	request := grantQuestionRequest{ctx: ctx, question: question, answer: make(chan bool, 1), ended: make(chan struct{})}
	defer close(request.ended)
	answer := m.waitTier(func() tea.Msg { return m.receiveGrantAnswer(ctx, request) }).(grantAnswer)
	return answer.yes, answer.err
}

func (m *Model) receiveGrantAnswer(ctx context.Context, request grantQuestionRequest) grantAnswer {
	select {
	case m.grantRequests <- request:
	case <-ctx.Done():
		return grantAnswer{err: ctx.Err()}
	case <-m.ctx.Done():
		return grantAnswer{err: m.ctx.Err()}
	}
	select {
	case yes := <-request.answer:
		if err := ctx.Err(); err != nil {
			return grantAnswer{err: err}
		}
		if err := m.ctx.Err(); err != nil {
			return grantAnswer{err: err}
		}
		return grantAnswer{yes: yes}
	case <-ctx.Done():
		return grantAnswer{err: ctx.Err()}
	case <-m.ctx.Done():
		return grantAnswer{err: m.ctx.Err()}
	}
}

func (m *Model) waitForGrantQuestion() tea.Cmd {
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		return m.waitTier(func() tea.Msg {
			select {
			case request := <-m.grantRequests:
				return request
			case <-ctx.Done():
				return nil
			}
		})
	})
}

func (m *Model) onGrantQuestion(request grantQuestionRequest) tea.Cmd {
	if request.ctx.Err() != nil || m.ctx.Err() != nil {
		return m.waitForGrantQuestion()
	}
	m.grantActive = request.answer
	m.modal = &grantQuestion{request: request, previous: m.modal, name: newInput("", request.question.Host)}
	m.quitArmed = false
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		return m.waitTier(func() tea.Msg {
			select {
			case <-request.ended:
				return grantQuestionEndedMsg{answer: request.answer}
			case <-ctx.Done():
				return nil
			}
		})
	})
}

func (m *Model) onGrantQuestionEnded(msg grantQuestionEndedMsg) tea.Cmd {
	if m.grantActive == nil || msg.answer == nil || m.grantActive != msg.answer {
		return nil
	}
	m.grantActive = nil
	if question, ok := m.modal.(*grantQuestion); ok && question.request.answer == msg.answer {
		m.modal = question.previous
	}
	return m.waitForGrantQuestion()
}

func (m *Model) retireGrantQuestion() {
	m.grantActive = nil
	for {
		question, ok := m.modal.(*grantQuestion)
		if !ok || question.request.ctx.Err() == nil && m.ctx.Err() == nil {
			return
		}
		question.answer(false)
		m.modal = question.previous
	}
}
