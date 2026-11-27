//go:build linux

package jail

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"
	"time"
)

type gvisorFreezerKernelV1 struct {
	mu                         sync.Mutex
	frozen, populated, stalled bool
	changed                    error
	writes                     []bool
	releases                   int
	thawed                     chan struct{}
}

func (k *gvisorFreezerKernelV1) check(bool) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.changed
}

func (k *gvisorFreezerKernelV1) write(freeze bool) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.writes = append(k.writes, freeze)
	if !k.stalled || !freeze {
		k.frozen = freeze
	}
	if !freeze && k.thawed != nil {
		select {
		case k.thawed <- struct{}{}:
		default:
		}
	}
	return nil
}

func (k *gvisorFreezerKernelV1) state() (bool, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.frozen, k.populated, nil
}

func (k *gvisorFreezerKernelV1) release() (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.releases++
	return true, nil
}

func (k *gvisorFreezerKernelV1) snapshot() ([]bool, int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.writes), k.releases
}

func TestGvisorFreezerRequiresKernelAcknowledgementAndRestoresOnCancel(t *testing.T) {
	kernel := &gvisorFreezerKernelV1{populated: true, stalled: true}
	freezer := &gvisorFreezerV1{control: kernel}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := freezer.freeze(ctx, 5000); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("write without frozen=1 was accepted: %v", err)
	}
	writes, _ := kernel.snapshot()
	if !slices.Equal(writes, []bool{true, false}) || freezer.frozen {
		t.Fatalf("cancel left an unjoined cgroup freeze: %v, owned=%v", writes, freezer.frozen)
	}
}

func TestGvisorFreezerLeaseExpiresWithoutAController(t *testing.T) {
	kernel := &gvisorFreezerKernelV1{populated: true, thawed: make(chan struct{}, 1)}
	freezer := &gvisorFreezerV1{control: kernel}
	if err := freezer.freeze(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	select {
	case <-kernel.thawed:
	case <-time.After(time.Second):
		t.Fatal("a lost controller left the process cgroup frozen")
	}
	freezer.mu.Lock()
	if freezer.frozen {
		t.Fatal("autonomous thaw did not join the kernel state")
	}
	freezer.mu.Unlock()
	if err := freezer.freeze(t.Context(), 5000); err == nil {
		t.Fatal("late renewal adopted a new freeze after autonomous thaw")
	}
	if err := freezer.releaseLease(); err != nil {
		t.Fatal(err)
	}
}

func TestGvisorFreezerRenewalRejectsAnExpiredLeaseAndIgnoresOldTimer(t *testing.T) {
	kernel := &gvisorFreezerKernelV1{populated: true}
	freezer := &gvisorFreezerV1{control: kernel}
	if err := freezer.freeze(t.Context(), 5000); err != nil {
		t.Fatal(err)
	}
	previous := freezer.generation
	if err := freezer.freeze(t.Context(), 5000); err != nil {
		t.Fatal(err)
	}
	freezer.expire(previous)
	writes, _ := kernel.snapshot()
	if !slices.Equal(writes, []bool{true}) {
		t.Fatalf("old expiry thawed a renewed copy: %v", writes)
	}
	freezer.mu.Lock()
	freezer.deadline = time.Now().Add(-time.Second)
	freezer.mu.Unlock()
	if err := freezer.freeze(t.Context(), 5000); err == nil {
		t.Fatal("expired continuity was renewed as a new valid copy")
	}
	writes, _ = kernel.snapshot()
	if !slices.Equal(writes, []bool{true, false}) {
		t.Fatalf("expired lease did not restore its original cgroup: %v", writes)
	}
}

func TestGvisorFreezerPreservesOutsideFreezeAndRejectsChangedCreator(t *testing.T) {
	for _, kernel := range []*gvisorFreezerKernelV1{{frozen: true, populated: true}, {changed: errors.New("original cgroup replaced")}} {
		freezer := &gvisorFreezerV1{control: kernel}
		if err := freezer.freeze(t.Context(), 5000); err == nil {
			t.Fatal("outside state became this runtime's freeze")
		}
		freezer.mu.Lock()
		err := freezer.thawLocked()
		freezer.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		writes, _ := kernel.snapshot()
		if len(writes) != 0 {
			t.Fatalf("unowned state was changed: %v", writes)
		}
	}
}

func TestGvisorFreezerRetainsCreationUntilKernelProvesEmpty(t *testing.T) {
	kernel := &gvisorFreezerKernelV1{populated: true}
	freezer := &gvisorFreezerV1{control: kernel}
	if err := freezer.closeEmpty(); err == nil {
		t.Fatal("a populated cgroup was released")
	}
	_, released := kernel.snapshot()
	if released != 0 || freezer.closed {
		t.Fatal("wait status substituted for native emptiness")
	}
	kernel.mu.Lock()
	kernel.populated = false
	kernel.mu.Unlock()
	if err := freezer.closeEmpty(); err != nil {
		t.Fatal(err)
	}
	_, released = kernel.snapshot()
	if released != 1 || !freezer.closed {
		t.Fatal("the same empty creation was not released once")
	}
}

func TestGvisorFreezerFailedThawRetainsTheSameCreator(t *testing.T) {
	kernel := &gvisorFreezerKernelV1{populated: true}
	freezer := &gvisorFreezerV1{control: kernel}
	if err := freezer.freeze(t.Context(), 5000); err != nil {
		t.Fatal(err)
	}
	kernel.mu.Lock()
	kernel.changed = errors.New("original native check is uncertain")
	kernel.mu.Unlock()
	if err := freezer.releaseLease(); err == nil {
		t.Fatal("an uncertain thaw was reported as completion")
	}
	if err := freezer.closeEmpty(); err == nil {
		t.Fatal("an uncertain frozen creator was released")
	}
	_, releases := kernel.snapshot()
	if releases != 0 || freezer.closed || !freezer.frozen {
		t.Fatal("failed thaw discarded the original owner")
	}
	kernel.mu.Lock()
	kernel.changed, kernel.populated = nil, false
	kernel.mu.Unlock()
	if err := freezer.releaseLease(); err != nil {
		t.Fatal(err)
	}
	if err := freezer.closeEmpty(); err != nil {
		t.Fatal(err)
	}
	_, releases = kernel.snapshot()
	if releases != 1 {
		t.Fatal("retry did not release the same empty native creation")
	}
}

func TestGvisorFreezeBoundsCannotOverflow(t *testing.T) {
	for _, maxMS := range []int{-1, 0, 5001, math.MaxInt} {
		kernel := &gvisorFreezerKernelV1{}
		freezer := &gvisorFreezerV1{control: kernel}
		if err := freezer.freeze(t.Context(), maxMS); err == nil {
			t.Fatalf("accepted lease %d", maxMS)
		}
		writes, _ := kernel.snapshot()
		if len(writes) != 0 {
			t.Fatal("invalid lease reached the kernel")
		}
	}
	command := &Cmd{pidfd: -1}
	if command.FreezeAvailableV1() || command.FreezeAllProcessesV1(t.Context(), 5000) == nil {
		t.Fatal("a copied command row minted gvisor freeze support")
	}
}

func TestGvisorNativeCgroupDecodersRefuseAmbiguityAndMissingAcknowledgements(t *testing.T) {
	for _, raw := range []string{"", "0::relative\n", "0::/one\n0::/two\n", "0::/one/../two\n", "0::/one\r\n"} {
		if _, err := decodeGvisorCgroupV1([]byte(raw)); err == nil {
			t.Fatalf("accepted native membership %q", raw)
		}
	}
	if got, err := decodeGvisorCgroupV1([]byte("0::/user.slice/unit.service\n")); err != nil || got != "/user.slice/unit.service" {
		t.Fatalf("actual membership: %q, %v", got, err)
	}
	for _, raw := range []string{"", "populated 0\n", "populated 0\nfrozen 2\n", "populated 0\nfrozen 0\nfrozen 1\n"} {
		if _, _, err := decodeGvisorCgroupEventsV1([]byte(raw)); err == nil {
			t.Fatalf("accepted native freeze claim %q", raw)
		}
	}
	if frozen, populated, err := decodeGvisorCgroupEventsV1([]byte("populated 1\nfrozen 1\nfuture 7\n")); err != nil || !frozen || !populated {
		t.Fatalf("actual kernel acknowledgement: %v, %v, %v", frozen, populated, err)
	}
}
