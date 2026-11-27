package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func placementStoreFixture(t *testing.T) (*placementStore, *autoPlacementCandidate) {
	t.Helper()
	candidate := placementCandidateFixture(t, nil, func(f *api.HostFacts) {
		f.Walls[0].Version = "original opaque version"
		f.Walls[0].Facts = map[string]string{"opaque": "original text"}
	})
	if reason, err := filterPlacementFacts(t.Context(), candidate, 0, false, true); err != nil || reason != "" {
		t.Fatal(reason, err)
	}
	operation, err := openSettingsOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operation.Close() })
	store, err := openPlacementStore(t.Context(), operation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	return store, candidate
}

func TestPlacementCachePreservesReceiptTimeAndCannotRenewChangedSource(t *testing.T) {
	store, candidate := placementStoreFixture(t)
	observed := candidate.observation
	if err := store.rememberFacts(t.Context(), []*hostFactsObservation{observed}, []Host{candidate.host}); err != nil {
		t.Fatal(err)
	}
	cached, err := store.cachedFacts(t.Context(), candidate.host)
	if err != nil || cached == nil || !cached.entry.ReceivedAt.Equal(observed.receivedAt) || cached.entry.Facts.Room != observed.facts.Room {
		t.Fatal("cache changed the original local receipt or room", err)
	}
	if cached.entry.Facts.Walls[0].Version != "" || cached.entry.Facts.Walls[0].Facts != nil {
		t.Fatal("cache retained unneeded opaque runtime text")
	}
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{candidate.host}); err != nil {
		t.Fatal(err)
	}
	if err := cached.check(t.Context()); err == nil {
		t.Fatal("a cache DTO renewed its original removed/re-added Source")
	}
}

func TestPlacementIndependentLocksCountOtherWritersBeforeRecording(t *testing.T) {
	first, candidate := placementStoreFixture(t)
	second, err := openPlacementStore(t.Context(), first.operation)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	type outcome struct {
		record *ownedPlacementRecord
		err    error
	}
	results := make(chan outcome, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for _, store := range []*placementStore{first, second} {
		go func() {
			ready.Done()
			<-start
			record, err := store.reservePlacement(t.Context(), candidate, "awake, image cached, most room")
			results <- outcome{record, err}
		}()
	}
	ready.Wait()
	close(start)
	var accepted *ownedPlacementRecord
	refused := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			if accepted != nil {
				t.Fatal("independent lock descriptors admitted more local in-flight bytes than observed room")
			}
			accepted = result.record
		} else {
			var user *UserError
			if !errors.As(result.err, &user) || user.Code != "no_room" {
				t.Fatal(result.err)
			}
			refused++
		}
	}
	if accepted == nil || refused != 1 {
		t.Fatal("both writers did not observe the same locked placement state")
	}
	if err := accepted.finish(t.Context(), "", true); err != nil {
		t.Fatal("own refusal could not remove only its original mark", err)
	}
	if _, err := first.reservePlacement(t.Context(), candidate, "awake, most room"); err != nil {
		t.Fatal("the exact own refusal left its in-flight bytes behind", err)
	}
}

func TestPlacementFutureStateAndCancellationNeverResetOrPublish(t *testing.T) {
	store, candidate := placementStoreFixture(t)
	path := filepath.Join(CacheDir(), "placements.json")
	before := []byte(`{"v":2,"future":{"opaque":true}}`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.reservePlacement(t.Context(), candidate, "awake, most room"); err == nil {
		t.Fatal("a future state became an empty new placement history")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("future placement state was reset", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.locked(cancelled, "placements.json", func(context.Context) error { t.Fatal("cancelled transaction entered its writer"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation did not win before the state write", err)
	}
	if _, _, err := store.read(t.Context(), "../foreign"); err == nil {
		t.Fatal("a host-controlled path became local cache access")
	}
}
