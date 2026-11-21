package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func imageTransferCallerV1(t *testing.T) {
	t.Helper()
	oldSelf, oldNamespaces := SelfCgroup, CgroupNamespaces
	SelfCgroup = func() ([]byte, error) { return []byte("0::/user.slice/user-1000.slice/session-1.scope\n"), nil }
	CgroupNamespaces = func() (string, string, error) { return "cgroup:[1]", "cgroup:[1]", nil }
	t.Cleanup(func() { SelfCgroup, CgroupNamespaces = oldSelf, oldNamespaces })
}
func imageTransferRequestV1(token string) image.Request {
	return image.Request{Ref: "registry.example/team/root:current", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", Transfer: token}
}
func queueImageFixtureV1(t *testing.T, b *bridge, kind string, id uint64, token string) func() {
	t.Helper()
	args := imageTransferRequestV1(token)
	var value any = args
	op := "image.pull"
	if kind == "begin" {
		op = "image.install"
		value = image.Install{Action: "begin", Request: &args}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	finish, err := b.queueImageOperationV1(proto.Request{ID: id, Op: op, Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	return finish
}
func TestImageAbortBeforeHandlerJoinsOriginalCorrelationV1(t *testing.T) {
	imageTransferCallerV1(t)
	for _, kind := range []string{"pull", "begin"} {
		t.Run(kind, func(t *testing.T) {
			b := &bridge{ctx: t.Context()}
			token := strings.Repeat("1", 32)
			finish := queueImageFixtureV1(t, b, kind, 1, token)
			defer finish()
			b.images.mu.Lock()
			job := b.images.jobs[token]
			b.images.mu.Unlock()
			result := make(chan error, 1)
			go func() {
				_, err := b.images.install(t.Context(), image.Install{Action: "abort", Token: token}, 2)
				result <- err
			}()
			<-job.ctx.Done()
			select {
			case err := <-result:
				t.Fatalf("abort returned before queued creator joined: %v", err)
			default:
			}
			b.images.mu.Lock()
			retained := b.images.jobs[token] == job
			b.images.mu.Unlock()
			if !retained {
				t.Fatal("abort deleted the correlation before the original creator joined")
			}
			finish() // The actual queued handler's terminal cleanup edge.
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			b.images.mu.Lock()
			_, retained = b.images.jobs[token]
			b.images.mu.Unlock()
			if retained {
				t.Fatal("joined abort retained a completed correlation")
			}
		})
	}
}
func TestImageAbortCannotReachAnotherConnectionV1(t *testing.T) {
	imageTransferCallerV1(t)
	original := &bridge{ctx: t.Context()}
	other := &bridge{ctx: t.Context()}
	token := strings.Repeat("2", 32)
	finish := queueImageFixtureV1(t, original, "pull", 1, token)
	defer finish()
	if _, err := other.images.install(t.Context(), image.Install{Action: "abort", Token: token}, 2); err == nil {
		t.Fatal("another connection cancelled the original transfer")
	}
	if err := original.images.jobs[token].ctx.Err(); err != nil {
		t.Fatalf("foreign abort reached the original handler: %v", err)
	}
}
func TestImageQueuedPinsAndDispatchCannotBeReplacedV1(t *testing.T) {
	imageTransferCallerV1(t)
	b := &bridge{ctx: t.Context()}
	token := strings.Repeat("3", 32)
	finish := queueImageFixtureV1(t, b, "begin", 7, token)
	defer finish()
	changed := imageTransferRequestV1(token)
	changed.PlatformDigest = "sha256:" + strings.Repeat("c", 64)
	if _, err := b.images.claimImageHandlerV1(changed, 7, "begin"); err == nil {
		t.Fatal("a queued begin changed its pinned platform")
	}
	if _, err := b.images.claimImageHandlerV1(imageTransferRequestV1(token), 8, "begin"); err == nil {
		t.Fatal("another request consumed the queued begin")
	}
	if _, err := b.images.claimImageHandlerV1(imageTransferRequestV1(token), 7, "pull"); err == nil {
		t.Fatal("a pull consumed an upload begin")
	}
	if _, err := b.images.claimImageHandlerV1(imageTransferRequestV1(token), 7, "begin"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.images.claimImageHandlerV1(imageTransferRequestV1(token), 7, "begin"); err == nil {
		t.Fatal("the original begin handler was consumed twice")
	}
}
func TestImageCommitAbortJoinsSameOperationV1(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	token := strings.Repeat("4", 32)
	job := &imageJob{ctx: ctx, cancel: cancel, correlation: token, kind: "begin", done: make(chan struct{})}
	close(job.done) // Begin has already returned its server token.
	s := &imageService{jobs: map[string]*imageJob{token: job}}
	job.operation.Lock() // An actual commit retains this until conversion returns.
	result := make(chan error, 1)
	go func() {
		_, err := s.install(t.Context(), image.Install{Action: "abort", Token: token}, 2)
		result <- err
	}()
	<-ctx.Done()
	select {
	case err := <-result:
		t.Fatalf("abort returned while the same converter operation was active: %v", err)
	default:
	}
	s.mu.Lock()
	retained := s.jobs[token] == job
	s.mu.Unlock()
	if !retained {
		t.Fatal("active commit owner was removed before join")
	}
	job.operation.Unlock()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
