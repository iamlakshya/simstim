package host

import (
	"testing"
	"time"
)

func TestNoneHelperExitClosesAdmissionBeforeEmptyJoin(t *testing.T) {
	registry := &noneCustodyRegistryV1{}
	registry.joinV1()
	if retained, err := registry.admitDeleteV1("later"); err == nil || retained != nil {
		t.Fatal("helper exit admitted a new owner after its empty join")
	}
}

func TestNoneHelperExitJoinsActualWorkerAndAllowsOnlySameRetry(t *testing.T) {
	registry := &noneCustodyRegistryV1{}
	original, err := registry.admitDeleteV1("primary")
	if err != nil {
		t.Fatal(err)
	}
	if other, err := registry.admitDeleteV1("primary"); err == nil || other != nil {
		t.Fatal("concurrent deletion acquired another original worker")
	}
	registry.mu.Lock()
	admissionChanged := registry.changed
	registry.mu.Unlock()
	joined := make(chan struct{})
	go func() {
		registry.joinV1()
		close(joined)
	}()
	select {
	case <-admissionChanged:
	case <-time.After(time.Second):
		t.Fatal("helper exit did not close admission")
	}
	if other, err := registry.admitDeleteV1("later"); err == nil || other != nil {
		t.Fatal("closed helper admitted a different cleanup worker")
	}
	select {
	case <-joined:
		t.Fatal("logical request end discarded an actual registered owner")
	default:
	}
	original.mu.Unlock()
	retry, err := registry.admitDeleteV1("primary")
	if err != nil || retry != original {
		t.Fatal("closed admission replaced or refused the SAME retained transaction", err)
	}
	foreign := &noneDeleteRetainedV1{}
	registry.finishDeleteV1("primary", foreign)
	select {
	case <-joined:
		t.Fatal("another worker retired the original owner")
	default:
	}
	registry.finishDeleteV1("primary", retry)
	retry.mu.Unlock()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("actual original worker retirement did not release exit")
	}
}

// EndBridge fixtures share a process. Reopen only after the real join returned
// with no original owner left; production has no reset or reopening API.
func reopenNoneCustodyForTestV1() {
	noneCustodyV1.mu.Lock()
	defer noneCustodyV1.mu.Unlock()
	if len(noneCustodyV1.deletes) != 0 || len(noneCustodyV1.stops) != 0 {
		panic("an EndBridge fixture still retains original None cleanup owners")
	}
	noneCustodyV1.closed = false
	noneCustodyV1.changedV1()
}
