package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
)

type snapshotFreezerFixtureV1 struct {
	sandbox
	mu             sync.Mutex
	freezes, thaws int
	failRenew      bool
}

func (f *snapshotFreezerFixtureV1) FreezeV1(ctx context.Context, maxMS int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.freezes++
	if maxMS != 5000 {
		return errors.New("wrong freeze bound")
	}
	if f.failRenew && f.freezes > 1 {
		return errors.New("original lease renewal failed")
	}
	return ctx.Err()
}
func (f *snapshotFreezerFixtureV1) ThawV1() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.thaws++
	return nil
}

func TestSnapshotRenewalCancelsCopyAndJoinsBeforeThaw(t *testing.T) {
	before := freezeRenewEveryV1
	freezeRenewEveryV1 = time.Millisecond
	t.Cleanup(func() { freezeRenewEveryV1 = before })
	runtime := &snapshotFreezerFixtureV1{failRenew: true}
	s := &Supervisor{sb: runtime, m: &host.Manifest{Wall: &host.WallRecord{Level: "process"}}, lifetime: t.Context()}
	joined := false
	err := s.withFrozenV1(t.Context(), func(ctx context.Context) error { <-ctx.Done(); joined = true; return ctx.Err() })
	if err == nil || !joined {
		t.Fatal("a failed renewal left the copy alive or reported success")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.freezes != 2 || runtime.thaws != 1 {
		t.Fatalf("physical lease calls freeze=%d thaw=%d", runtime.freezes, runtime.thaws)
	}
	if !s.heartbeatPause.IsZero() {
		t.Fatal("the original heartbeat pause remained after thaw")
	}
}

func TestSnapshotWaitHonoursRequestCancellation(t *testing.T) {
	s := &Supervisor{lifetime: t.Context()}
	release, err := s.acquireSnapshotV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if next, err := s.acquireSnapshotV1(ctx); err == nil || next != nil {
		t.Fatal("cancelled request acquired the busy snapshot turn")
	}
}

func TestCheckpointDefaultAndExplicitOff(t *testing.T) {
	if keep, err := checkpointKeepV1(nil); err != nil || keep != 10 {
		t.Fatalf("default keep=%d err=%v", keep, err)
	}
	s := &Supervisor{m: &host.Manifest{Wall: &host.WallRecord{Level: "process"}}}
	if _, err := s.checkpointPolicyV1(); err == nil {
		t.Fatal("a process wall enabled disk checkpoints")
	}
}

func TestSnapshotHandoffRenewsTheRetainedLeaseAndJoinsThaw(t *testing.T) {
	runtime := &snapshotFreezerFixtureV1{}
	s := &Supervisor{sb: runtime, m: &host.Manifest{Wall: &host.WallRecord{Level: "process"}}, lifetime: t.Context()}
	var actual *snapshotFreezeLeaseV1
	if err := s.withFrozenLeaseV1(t.Context(), func(lease *snapshotFreezeLeaseV1) error {
		actual = lease
		if err := lease.renewForHandoffV1(t.Context()); err != nil {
			return err
		}
		return lease.checkV1()
	}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	freezes, thaws := runtime.freezes, runtime.thaws
	runtime.mu.Unlock()
	if freezes != 2 || thaws != 1 {
		t.Fatalf("handoff did not renew the same physical lease and thaw once: %d/%d", freezes, thaws)
	}
	if err := actual.checkV1(); err == nil {
		t.Fatal("a completed callback retained a physical freeze")
	}
	if err := actual.renewForHandoffV1(t.Context()); err == nil {
		t.Fatal("handoff renewed a lease after its actual thaw")
	}
	if err := (&snapshotFreezeLeaseV1{}).renewForHandoffV1(t.Context()); err == nil {
		t.Fatal("deadline data created an original physical freeze")
	}
}
