package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/runtimecache"
)

// These lower job-lifetime oracles create one real reserved queue role. They
// supply no native store, process exit, session operation or capacity proof.
func runtimeJobFixtureV1(t *testing.T, started bool) (*runtimeService, string, *runtimeJob, int) {
	t.Helper()
	before := len(runtimeSlots)
	service := &runtimeService{}
	service.mu.Lock()
	err := service.reserve()
	service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	job := &runtimeJob{ctx: ctx, cancel: cancel, requestID: 17, started: started, done: make(chan struct{})}
	token := strings.Repeat("a", 32)
	service.jobs = map[string]*runtimeJob{token: job}
	t.Cleanup(func() { service.finishRuntimeJobV1(token, job) })
	return service, token, job, before
}

func TestRuntimeQueuedAbortSettlesOriginalSlotBeforeDelayedDispatch(t *testing.T) {
	service, token, job, before := runtimeJobFixtureV1(t, false)
	if !service.cancelRuntimeJobV1(token) {
		t.Fatal("queued cancellation lost its original job")
	}
	select {
	case <-job.done:
	default:
		t.Fatal("queued cancellation did not settle its exact role")
	}
	if _, err := service.fetchRegistered(runtimecache.RuntimeRequest{Transfer: token}, 17); err == nil {
		t.Fatal("delayed dispatch revived an aborted queue role")
	}
	service.close()
	service.close()
	if len(runtimeSlots) != before {
		t.Fatal("queued abort or duplicate close consumed another slot")
	}
}

func TestRuntimeRunningAbortWaitsForOriginalCleanup(t *testing.T) {
	service, token, job, before := runtimeJobFixtureV1(t, true)
	cleanup := make(chan struct{})
	finished := make(chan struct{})
	go func() { <-job.ctx.Done(); <-cleanup; service.finishRuntimeJobV1(token, job); close(finished) }()
	aborted := make(chan bool, 1)
	go func() { aborted <- service.cancelRuntimeJobV1(token) }()
	<-job.ctx.Done()
	select {
	case <-aborted:
		t.Fatal("abort reported completion while original cleanup was held")
	case <-time.After(20 * time.Millisecond):
	}
	close(cleanup)
	select {
	case ok := <-aborted:
		if !ok {
			t.Fatal("running cancellation lost its original job")
		}
	case <-time.After(time.Second):
		t.Fatal("abort kept s.mu while waiting for real cleanup")
	}
	<-finished
	service.finishRuntimeJobV1(token, job)
	if len(runtimeSlots) != before {
		t.Fatal("running cleanup released the reserved slot twice")
	}
}

func TestRuntimeCloseAndAbortJoinSameRunningCleanup(t *testing.T) {
	service, token, job, before := runtimeJobFixtureV1(t, true)
	cleanup := make(chan struct{})
	go func() { <-job.ctx.Done(); <-cleanup; service.finishRuntimeJobV1(token, job) }()
	closed := make(chan struct{})
	go func() { service.close(); close(closed) }()
	<-job.ctx.Done()
	aborted := make(chan struct{})
	go func() { service.cancelRuntimeJobV1(token); close(aborted) }()
	select {
	case <-closed:
		t.Fatal("service close passed original fetch cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(cleanup)
	for _, done := range []chan struct{}{closed, aborted} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("concurrent close/abort did not join the same job")
		}
	}
	service.close()
	if len(runtimeSlots) != before {
		t.Fatal("close and abort released another job's slot")
	}
}
