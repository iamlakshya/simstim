//go:build linux

package host

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/publicationlease"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

func TestRuntimeFetchCleanupErrorCannotReleaseSlotOrAcknowledgeAbortV1(t *testing.T) {
	receipt, loan, _, _ := agentsFailedCleanupFixtureV1(t)
	service := &runtimeService{}
	before := len(runtimeSlots)
	service.mu.Lock()
	err := service.reserve()
	service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &runtimeJob{ctx: ctx, cancel: cancel, started: true, done: make(chan struct{}), resource: &runtimeOwnedPart{publication: receipt.publication}}
	service.jobs = map[string]*runtimeJob{"actual": job}
	if err := service.finishRuntimeJobV1("actual", job); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal(err)
	}
	if service.jobs["actual"] != job || len(runtimeSlots) != before+1 {
		t.Fatal("handler completion released unfinished physical cleanup")
	}
	if found, err := service.cancelRuntimeJobCleanupV1("actual"); !found || !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("abort falsely acknowledged unfinished cleanup", found, err)
	}
	loan.Close()
	if found, err := service.cancelRuntimeJobCleanupV1("actual"); !found || err != nil {
		t.Fatal(found, err)
	}
	if len(service.jobs) != 0 || len(runtimeSlots) != before {
		t.Fatal("same cleanup retry did not release original slot exactly once")
	}
}

func TestRuntimeOwnerRetainsSamePublicationAndProcessSlotAfterCloseFailureV1(t *testing.T) {
	receipt, loan, _, _ := agentsFailedCleanupFixtureV1(t)
	service := &runtimeService{}
	before := len(runtimeSlots)
	owner, err := service.newRuntimeOwnerV1()
	if err != nil {
		t.Fatal(err)
	}
	store, err := runtimecache.Open(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	item := supply.NewItem(supply.KindUserns, "fixture", "1.2.3", "amd64", []byte("bytes"), supply.ReleaseFolder("1.2.3")+"/fixture")
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	owner.part, owner.store, owner.publication = part, store, receipt.publication
	service.parts = map[string]*runtimeOwnedPart{"actual": owner}
	if err := service.finishRuntimeOwnerV1(owner); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("live helper fence lost its original owner", err)
	}
	if service.parts["actual"] != owner || len(service.owned) != 1 || len(runtimeSlots) != before+1 || owner.publication != receipt.publication {
		t.Fatal("cleanup failure released the actual object or its slot")
	}
	if owner.part != nil || owner.store != nil {
		t.Fatal("successful earlier steps were not recorded on same stack")
	}
	loan.Close()
	if err := service.finishRuntimeOwnerV1(owner); err != nil {
		t.Fatal(err)
	}
	if len(service.parts) != 0 || len(service.owned) != 0 || len(runtimeSlots) != before {
		t.Fatal("successful retry did not settle exact slot and object")
	}
}

func TestImagePartialOpenRetainsOriginalPublicationOnCleanupErrorV1(t *testing.T) {
	receipt, loan, _, _ := agentsFailedCleanupFixtureV1(t)
	service := &imageService{publication: receipt.publication}
	if err := service.closeImageOpenV1(receipt.publication, nil); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal(err)
	}
	if service.publication != receipt.publication || service.closeErr == nil {
		t.Fatal("partial open discarded original publication or pending cleanup state")
	}
	if _, err := service.open(t.Context()); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("a new open bypassed the SAME failed cleanup", err)
	}
	loan.Close()
	if err := service.closeImageOpenV1(receipt.publication, nil); err != nil {
		t.Fatal(err)
	}
	if service.publication != nil || service.closeErr != nil {
		t.Fatal("successful retry kept completed publication or failed state")
	}
}
