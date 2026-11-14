package tui

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

func grantQuestionModel(t *testing.T) *Model {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	model := New(t.Context(), newFakeBackend(), Options{})
	t.Cleanup(model.Close)
	return model
}

func grantPrompt(kind client.ConfirmKind) *client.ConfirmError {
	return &client.ConfirmError{Host: "box", Kind: kind, Prompt: "Send TOKEN to box? Its owner can read it.", Err: &client.UserError{Msg: "a live grant answer is needed", Hint: "Answer in the launcher.", Code: "tier_refused", Kind: client.KindRefused}}
}

func TestGrantSheetHasNoDefaultAndRequiresTheExactHost(t *testing.T) {
	for _, row := range []struct {
		kind client.ConfirmKind
		key  string
		want bool
	}{
		{client.ConfirmYesNo, "enter", false}, {client.ConfirmYesNo, "n", false}, {client.ConfirmYesNo, "esc", false}, {client.ConfirmYesNo, "y", true},
	} {
		t.Run(string(row.kind)+"/"+row.key, func(t *testing.T) {
			model := grantQuestionModel(t)
			request := grantQuestionRequest{ctx: model.ctx, question: grantPrompt(row.kind), answer: make(chan bool, 1), ended: make(chan struct{})}
			previous := &helpSheet{}
			sheet := &grantQuestion{request: request, previous: previous}
			model.modal = sheet
			sheet.key(model, keyMsg(row.key))
			select {
			case yes := <-request.answer:
				if yes != row.want {
					t.Fatalf("answer %v, want %v", yes, row.want)
				}
			default:
				t.Fatal("the live sheet did not answer")
			}
			if model.modal != previous {
				t.Fatal("the live sheet did not restore its previous owner")
			}
		})
	}
	model := grantQuestionModel(t)
	request := grantQuestionRequest{ctx: model.ctx, question: grantPrompt(client.ConfirmHostName), answer: make(chan bool, 1), ended: make(chan struct{})}
	sheet := &grantQuestion{request: request, name: newInput("Box", "")}
	model.modal = sheet
	sheet.key(model, keyMsg("enter"))
	select {
	case <-request.answer:
		t.Fatal("another spelling approved the host")
	default:
	}
	if !sheet.wrong || model.modal != sheet {
		t.Fatal("an inexact typed answer closed the sheet")
	}
	sheet.name.set("box")
	sheet.key(model, keyMsg("enter"))
	select {
	case yes := <-request.answer:
		if !yes {
			t.Fatal("an exact live host answer was lost")
		}
	default:
		t.Fatal("the exact host was not answered")
	}
}

func TestGrantWaitCancellationWinsAnAlreadyQueuedYes(t *testing.T) {
	model := grantQuestionModel(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan grantAnswer, 1)
	go func() {
		yes, err := model.askGrant(ctx, grantPrompt(client.ConfirmYesNo))
		done <- grantAnswer{yes: yes, err: err}
	}()
	var request grantQuestionRequest
	select {
	case request = <-model.grantRequests:
	case <-time.After(5 * time.Second):
		t.Fatal("the original operation did not request a live answer")
	}
	cancel()
	request.answer <- true
	select {
	case result := <-done:
		if result.yes || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("cancelled answer became approval: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled answer did not join")
	}
	select {
	case <-request.ended:
	default:
		t.Fatal("the original question remained live")
	}
}

func TestGrantConfirmationRejectsAnUnscopedQuestion(t *testing.T) {
	model := grantQuestionModel(t)
	ctx, cancel := context.WithCancel(client.WithGrantRequest(t.Context(), client.NewGrantRequest(false, false)))
	defer cancel()
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- model.grantQuestions(ctx, func(context.Context) error { calls.Add(1); return grantPrompt(client.ConfirmYesNo) })
	}()
	var request grantQuestionRequest
	select {
	case request = <-model.grantRequests:
	case <-time.After(5 * time.Second):
		t.Fatal("the operation did not request consent")
	}
	model.onGrantQuestion(request)
	sheet := model.modal.(*grantQuestion)
	sheet.key(model, keyMsg("y"))
	select {
	case err := <-done:
		var user *client.UserError
		if !errors.As(err, &user) || user.Code != "grant_confirmation" || user.Kind != client.KindRefused || calls.Load() != 1 {
			t.Fatalf("an unscoped question gained authority: calls=%d error=%v", calls.Load(), err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the rejected confirmation did not join")
	}
}

func TestGrantQuestionStopsWithItsOperationOrModel(t *testing.T) {
	for _, stop := range []string{"operation", "close"} {
		t.Run(stop, func(t *testing.T) {
			model := grantQuestionModel(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- model.grantQuestions(ctx, func(context.Context) error { calls.Add(1); return grantPrompt(client.ConfirmYesNo) })
			}()
			var request grantQuestionRequest
			select {
			case request = <-model.grantRequests:
			case <-time.After(5 * time.Second):
				t.Fatal("no live question")
			}
			model.onGrantQuestion(request)
			if stop == "operation" {
				cancel()
			} else {
				model.Close()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
					t.Fatalf("the stopped question retried: calls=%d err=%v", calls.Load(), err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the stopped original operation did not join")
			}
			model.retireGrantQuestion()
			if _, ok := model.modal.(*grantQuestion); ok {
				t.Fatal("an ended question survived the next program")
			}
		})
	}
}

func TestStaleGrantEndCannotReplaceOrRearmAnotherQuestion(t *testing.T) {
	model := grantQuestionModel(t)
	if model.onGrantQuestionEnded(grantQuestionEndedMsg{}) != nil {
		t.Fatal("an empty grant end created a listener without an owner")
	}
	first := make(chan bool, 1)
	current := make(chan bool, 1)
	sheet := &grantQuestion{request: grantQuestionRequest{ctx: model.ctx, question: grantPrompt(client.ConfirmYesNo), answer: current}}
	model.modal, model.grantActive = sheet, current
	if cmd := model.onGrantQuestionEnded(grantQuestionEndedMsg{answer: first}); cmd != nil || model.modal != sheet || model.grantActive != current {
		t.Fatal("an obsolete grant end replaced the current owner or created another listener")
	}
}

func TestGrantOverlayPreservesPolicyOperationOwnership(t *testing.T) {
	hs, _, policy, _ := policyLifecycleFixture(t)
	ctx, seq := policy.begin(hs.m)
	request := grantQuestionRequest{ctx: ctx, question: grantPrompt(client.ConfirmYesNo), answer: make(chan bool, 1), ended: make(chan struct{})}
	hs.m.onGrantQuestion(request)
	hs.m.retirePolicy(policy)
	if policyOwner(hs.m.modal) != policy || policy.closed || policy.seq != seq || ctx.Err() != nil {
		t.Fatal("a temporary consent sheet cancelled the original policy operation")
	}
	hs.m.disposeModal()
	if !policy.closed || ctx.Err() == nil {
		t.Fatal("replacing consent did not cancel its underlying policy work")
	}
	select {
	case yes := <-request.answer:
		if yes {
			t.Fatal("replacement approved the grant")
		}
	default:
		t.Fatal("replacement left a live answer wait")
	}
}
