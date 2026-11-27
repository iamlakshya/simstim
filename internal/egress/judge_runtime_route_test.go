package egress

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

func TestClassifierEarlyUnsureKeepsOriginalTotalDeadline(t *testing.T) {
	server, _ := classifierFixture(t, "50ms", 20)
	var calls atomic.Int32
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(context.Context, classify.Input) (classify.Result, error) {
			calls.Add(1)
			return classify.Result{Action: "unsure", Reason: "insufficient evidence", Scope: "once"}, nil
		}), nil
	}
	request := Request{Host: "uncertain.example", Port: 443}
	started := time.Now()
	decision := server.awaitClassifierUnknown(t.Context(), request, request.Host, "explicit", nil, func(engine *Engine, candidate Request) Decision {
		return engine.evaluate(candidate, nil, evalPrecheck)
	})
	if decision.Allow || calls.Load() != 1 || time.Since(started) > time.Second {
		t.Fatal("early unsure renewed its deadline or repeated a judge call")
	}
	server.classifier.close()
}

func TestClassifierWorkSharesActualOriginSlots(t *testing.T) {
	server, _ := classifierFixture(t, "30ms", 20)
	for range cap(server.originSem) {
		server.originSem <- struct{}{}
	}
	var calls atomic.Int32
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(context.Context, classify.Input) (classify.Result, error) {
			calls.Add(1)
			return classify.Result{Action: "deny", Scope: "once"}, nil
		}), nil
	}
	server.runUnknownClassifier(t.Context(), Request{Host: "slots.example", Port: 443}, "explicit", "slots.example")
	server.classifier.close()
	if calls.Load() != 0 || len(server.originSem) != cap(server.originSem) {
		t.Fatal("classifier bypassed or released another physical origin's slot")
	}
	for range cap(server.originSem) {
		<-server.originSem
	}
}

func TestClassifierSourceRetirementJoinsWholeJobBeforeReleasingSlot(t *testing.T) {
	server, _ := classifierFixture(t, "2s", 20)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(ctx context.Context, _ classify.Input) (classify.Result, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return classify.Result{}, ctx.Err()
		}), nil
	}
	caller := make(chan struct{})
	go func() {
		server.runUnknownClassifier(t.Context(), Request{Host: "retire.example", Port: 443}, "explicit", "retire.example")
		close(caller)
	}()
	<-started
	joined := make(chan error, 1)
	go func() {
		if err := server.policyMu.LockContext(t.Context()); err != nil {
			joined <- err
			return
		}
		err := server.retireJudgeSourcesLocked(t.Context(), nil)
		server.policyMu.Unlock()
		joined <- err
	}()
	<-cancelled
	if len(server.originSem) != 1 {
		t.Fatal("cancelled work released its physical capacity before join")
	}
	select {
	case <-joined:
		t.Fatal("source retirement returned before the actual adapter joined")
	default:
	}
	close(release)
	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retired whole-job owner did not join")
	}
	<-caller
	server.classifier.close()
	if len(server.originSem) != 0 || len(server.flows.snapshot()) != 0 {
		t.Fatal("joined classifier retained a physical owner or origin slot")
	}
}
