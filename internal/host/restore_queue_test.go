package host

import (
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

func TestRestoreQueueBoundsColdStartsAndKeepsActivityOrder(t *testing.T) {
	at := time.Unix(100, 0)
	candidates := []restoreCandidateV1{{"oldest", at}, {"recent", at.Add(2 * time.Hour)}, {"middle", at.Add(time.Hour)}, {"last", time.Time{}}}
	entered := make(chan string, len(candidates))
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	done := make(chan []RestoreResult, 1)
	var mu sync.Mutex
	active, maximum := 0, 0
	go func() {
		done <- runRestoreCandidatesV1(candidates, func(id string) (RestoreResult, bool) {
			mu.Lock()
			active++
			maximum = max(maximum, active)
			mu.Unlock()
			entered <- id
			<-release
			mu.Lock()
			active--
			mu.Unlock()
			return RestoreResult{ID: id}, true
		})
	}()
	next := func() string {
		select {
		case id := <-entered:
			return id
		case <-time.After(10 * time.Second):
			t.Fatal("the bounded restore workers did not enter")
			return ""
		}
	}
	pair := []string{next(), next()}
	slices.Sort(pair)
	if !slices.Equal(pair, []string{"middle", "recent"}) {
		t.Fatalf("older work passed recent activity: %v", pair)
	}
	select {
	case id := <-entered:
		t.Fatalf("a third cold start entered while both slots were held: %s", id)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	var rows []RestoreResult
	select {
	case rows = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the released restore workers did not join")
	}
	if maximum != 2 || len(rows) != 4 || rows[0].ID != "recent" || rows[1].ID != "middle" || rows[2].ID != "oldest" || rows[3].ID != "last" {
		t.Fatalf("restore order or concurrency changed: maximum %d, rows %+v", maximum, rows)
	}
}

func TestLazyRestoreSelectsWorkingChildAndPreservesEndedIntent(t *testing.T) {
	primary := autoRestoreFixture("")
	primary.Guest = &GuestClaims{State: StateIdle}
	child := autoRestoreFixture(primary.ID)
	child.Guest = &GuestClaims{State: StateWorking}
	for _, member := range []*Manifest{primary, child} {
		if err := member.Save(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(member.ID)) })
	}
	if working, err := sandboxWasWorkingV1(primary); err != nil || !working {
		t.Fatal("a working child did not wake its shared sandbox", err)
	}
	child.Life.End.Claimed = true
	if err := child.Save(); err != nil {
		t.Fatal(err)
	}
	if working, err := sandboxWasWorkingV1(primary); err != nil || working {
		t.Fatal("a naturally ended child became automatic work", err)
	}
	primary.Guest = nil
	primary.Status.State = StateWorking
	if working, err := sandboxWasWorkingV1(primary); err != nil || working {
		t.Fatal("a controller status replaced the guest's last claimed state", err)
	}
}

func TestIdleDecisionNeverExpiresAnOffSetting(t *testing.T) {
	at := time.Unix(100, 0)
	for _, after := range []time.Duration{0, -time.Hour} {
		if _, stop := IdleDecide(at.Add(time.Hour), IdleState{BootID: "boot", BusyAt: at}, "boot", false, after); stop {
			t.Fatal("an off idle setting expired")
		}
	}
}
