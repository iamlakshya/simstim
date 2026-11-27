package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
	"github.com/iamlakshya/simstim/internal/proto"
)

type runtimeJudge func(context.Context, classify.Input) (classify.Result, error)

func (judge runtimeJudge) Decide(ctx context.Context, input classify.Input) (classify.Result, error) {
	return judge(ctx, input)
}

func classifierFixture(t *testing.T, timeout string, rate int) (*Server, *boundControlFixture) {
	t.Helper()
	fixture := newBoundControlFixture(t, true)
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	args.Initial.Policy.Order = []api.LayerRef{{Kind: api.LayerAll}}
	args.Initial.Policy.Unknown = api.UnknownClassify
	args.Initial.Policy.Classifier = judgeSettings()
	scope := "once"
	args.Initial.Policy.Classifier.MaxScope = &scope
	args.Initial.Policy.Classifier.Timeout = &timeout
	args.Initial.Policy.Classifier.Rate = &rate
	args.Initial.JudgeMetadata = classify.Metadata{Agent: "shell", Task: "user task with ABCD0000", RepositoryRemote: "https://user:ABCD0000@repo.example/team.git", Ecosystems: []string{"go"}}
	if _, err := fixture.server.initialiseBoundControl(memberJSON(t, args)); err != nil {
		t.Fatal(err)
	}
	if fixture.server.classifier == nil || fixture.server.holds == nil {
		t.Fatal("actual Server constructor omitted classifier/hold ownership")
	}
	return fixture.server, fixture
}

func waitClassifierIdle(t *testing.T, runtime *classifierRuntime) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		active := runtime.active
		runtime.mu.Unlock()
		if active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("owned classifier work did not physically finish")
}

func waitClassifierWaiters(t *testing.T, server *Server, want uint32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := server.holds.snapshot()
		if len(snapshot.Requests) == 1 && snapshot.Requests[0].Waiting == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("actual held-flow waiter count did not match active callers")
}

func TestClassifierRuntimeFixedDeniesRateZeroAndFullSourceGatePrecedeJudge(t *testing.T) {
	server, _ := classifierFixture(t, "1s", 0)
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		t.Fatal("fixed deny or zero rate called judge")
		return nil, nil
	}
	for _, host := range []string{"webhook.site", "192.0.2.1", "docs.example"} {
		outcome := server.runUnknownClassifier(t.Context(), Request{Host: host, Port: 443}, "explicit", host)
		if outcome.Applied || outcome.Decision.Allow {
			t.Fatal("fixed or rate refusal supplied permission")
		}
	}
	server, _ = classifierFixture(t, "1s", 20)
	var calls atomic.Int32
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(_ context.Context, input classify.Input) (classify.Result, error) {
			calls.Add(1)
			if input.Task != "" || strings.Contains(input.RepositoryRemote, "ABCD0000") || input.Agent != "shell" || input.RepositoryRemote != "https://repo.example/team.git" {
				t.Error("source metadata entered judge without original-value checks")
			}
			return classify.Result{Action: "deny", Reason: "ABCD0000", Scope: "session"}, nil
		}), nil
	}
	outcome := server.runUnknownClassifier(t.Context(), Request{Host: "docs.example", Port: 443}, "explicit", "docs.example")
	if calls.Load() != 1 || outcome.Applied || outcome.Decision.Allow || outcome.Result.Scope != "once" || strings.Contains(outcome.Result.Reason, "ABCD0000") {
		t.Fatal("typed source gate, scope clamp or refusal failed")
	}
}

func TestClassifierRuntimeCoalescesExistingWorkAndOneWaiterCannotCancelAnother(t *testing.T) {
	server, _ := classifierFixture(t, "2s", 20)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(ctx context.Context, _ classify.Input) (classify.Result, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			select {
			case <-ctx.Done():
				return classify.Result{}, ctx.Err()
			case <-release:
				return classify.Result{Action: "deny", Reason: "controlled refusal", Scope: "once"}, nil
			}
		}), nil
	}
	request := Request{Host: "shared.example", Port: 443}
	first := make(chan classifierOutcome, 1)
	go func() { first <- server.runUnknownClassifier(t.Context(), request, "explicit", request.Host) }()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	secondDone := make(chan classifierOutcome, 1)
	go func() { secondDone <- server.runUnknownClassifier(ctx, request, "explicit", request.Host) }()
	waitClassifierWaiters(t, server, 2)
	cancel()
	second := <-secondDone
	if second.Decision.Allow || calls.Load() != 1 {
		t.Fatal("waiter cancellation duplicated or allowed classifier work")
	}
	waitClassifierWaiters(t, server, 1)
	close(release)
	select {
	case actual := <-first:
		if actual.Result.Action != "deny" {
			t.Fatal("second waiter cancelled original judge")
		}
	case <-time.After(time.Second):
		t.Fatal("shared judge did not finish")
	}
	if server.holds.refusedCount(server.now()) != 1 {
		t.Fatal("same destination was counted as multiple classifier refusals")
	}
}

func TestClassifierRuntimeTransparentDeadlineStillPublishesActualLateOnceGrant(t *testing.T) {
	server, _ := classifierFixture(t, "4s", 20)
	release := make(chan struct{})
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(ctx context.Context, _ classify.Input) (classify.Result, error) {
			select {
			case <-ctx.Done():
				return classify.Result{}, ctx.Err()
			case <-release:
				return classify.Result{Action: "allow", Reason: "controlled documentation task", Scope: "session"}, nil
			}
		}), nil
	}
	request := Request{Host: "late.example", Port: 443}
	started := time.Now()
	outcome := server.runUnknownClassifier(t.Context(), request, "transparent", request.Host)
	if !outcome.WaitExpired || outcome.Applied || outcome.Decision.Allow || time.Since(started) > 3500*time.Millisecond {
		t.Fatal("transparent classifier flow exceeded its original three-second budget")
	}
	waitClassifierWaiters(t, server, 0)
	if server.currentEngine().Decide(request).Allow {
		t.Fatal("late grant existed before the actual callback")
	}
	close(release)
	waitClassifierIdle(t, server.classifier)
	engine := server.currentEngine()
	first, second := engine.NewPermit(), engine.NewPermit()
	defer first.Close()
	defer second.Close()
	if !engine.DecideWithPermit(request, first).Allow || engine.DecideWithPermit(request, second).Allow {
		t.Fatal("real late callback failed to install exactly one shared-ledger once grant")
	}
	first.Commit()
	first.Close()
	if engine.Decide(request).Allow {
		t.Fatal("spent classifier once permission was reusable")
	}
}

func TestClassifierRuntimeFailureDeadlineAndCloseJoinConfiguredWork(t *testing.T) {
	server, _ := classifierFixture(t, "2s", 20)
	started, stopped := make(chan struct{}), make(chan struct{})
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(ctx context.Context, _ classify.Input) (classify.Result, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return classify.Result{}, ctx.Err()
		}), nil
	}
	done := make(chan classifierOutcome, 1)
	go func() {
		done <- server.runUnknownClassifier(t.Context(), Request{Host: "timeout.example", Port: 443}, "explicit", "timeout.example")
	}()
	<-started
	server.classifier.close()
	select {
	case <-stopped:
	default:
		t.Fatal("runtime close returned before its actual adapter stopped")
	}
	select {
	case outcome := <-done:
		if outcome.Applied || outcome.Decision.Allow {
			t.Fatal("closed runtime allowed a flow")
		}
	case <-time.After(time.Second):
		t.Fatal("closed runtime retained its original waiter")
	}
	server.classifier.mu.Lock()
	active := server.classifier.active
	server.classifier.mu.Unlock()
	if active != 0 {
		t.Fatal("runtime close retained active work")
	}
}

func TestClassifierRuntimeConfiguredDeadlineCannotPublishAnUnfinishedAnswer(t *testing.T) {
	server, _ := classifierFixture(t, "30ms", 20)
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(ctx context.Context, _ classify.Input) (classify.Result, error) {
			<-ctx.Done()
			return classify.Result{}, ctx.Err()
		}), nil
	}
	started := time.Now()
	outcome := server.runUnknownClassifier(t.Context(), Request{Host: "deadline.example", Port: 443}, "explicit", "deadline.example")
	if outcome.Applied || outcome.Decision.Allow || time.Since(started) > time.Second {
		t.Fatal("configured classifier deadline was renewed or unfinished answer allowed")
	}
	server.classifier.close()
}

func TestClassifierRuntimeUnavailableElectionEmitsAtMostOneAcceptedControlFrame(t *testing.T) {
	server, _ := classifierFixture(t, "1s", 20)
	var output bytes.Buffer
	server.ctlMu.Lock()
	server.ctl = proto.NewWriter(&output)
	server.ctlStream = nil
	server.ctlMu.Unlock()
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(context.Context, classify.Input) (classify.Result, error) {
			return classify.Result{}, errors.New("controlled malformed/unavailable judge")
		}), nil
	}
	for _, host := range []string{"first.example", "second.example"} {
		server.runUnknownClassifier(t.Context(), Request{Host: host, Port: 443}, "explicit", host)
	}
	server.classifier.close()
	server.ctlMu.Lock()
	captured := append([]byte(nil), output.Bytes()...)
	server.ctlMu.Unlock()
	reader := bufio.NewReader(bytes.NewReader(captured))
	unavailable := 0
	for {
		frame, err := proto.Read(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(frame.Payload, &event) != nil {
			t.Fatal("invalid actual control event")
		}
		if event.Event == "judge_unavailable" {
			unavailable++
		}
	}
	if unavailable != 1 {
		t.Fatal("unavailable notification was lost or duplicated")
	}
	if strings.Contains(string(captured), "ABCD0000") {
		t.Fatal("actual classifier audit frame retained a source credential")
	}
}

func TestClassifierRuntimeInvalidAdapterCannotTriggerOptionalFactIO(t *testing.T) {
	server, _ := classifierFixture(t, "1s", 20)
	policy := api.ClonePolicy(server.currentMembers().metadata.Policy)
	enabled := true
	policy.Classifier.RDAP = &enabled
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy})); err != nil {
		t.Fatal(err)
	}
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return nil, errors.New("controlled invalid adapter")
	}
	server.classifier.facts.testFetch = func(context.Context, string, int64) ([]byte, error) {
		t.Error("invalid adapter triggered optional RDAP IO")
		return nil, errors.New("unexpected lookup")
	}
	outcome := server.runUnknownClassifier(t.Context(), Request{Host: "docs.example", Port: 443}, "explicit", "docs.example")
	if outcome.Decision.Allow || outcome.Applied {
		t.Fatal("invalid adapter supplied permission")
	}
	server.classifier.close()
}
