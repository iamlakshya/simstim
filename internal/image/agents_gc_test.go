//go:build (linux || darwin) && (amd64 || arm64)

package image

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAgentsGCRechecksStoppedReferencesAndUnpublishedSharedPins(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	oldBuild, _ := agentsStoreBuilder(t, "old executable")
	newBuild, _ := agentsStoreBuilder(t, "new executable")
	old, running, err := store.EnsureAgentsV1(t.Context(), runtime.GOARCH, oldBuild)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	newest, selected, err := store.EnsureAgentsV1(t.Context(), runtime.GOARCH, newBuild)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	used := map[string]string{old.Content.SHA256: "stopped-session"}
	refs := func() (map[string]string, error) { return used, nil }
	for _, phase := range []string{"stopped-reference", "unpublished-client-pin"} {
		removed, err := store.PruneAgentsV1(t.Context(), refs)
		if err != nil || len(removed) != 0 {
			t.Fatal("retained agents disk was collected", phase, removed, err)
		}
		used = map[string]string{}
	}
	if err := running.Close(); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneAgentsV1(t.Context(), refs)
	if err != nil || len(removed) != 1 || removed[0] != old.Content.SHA256 {
		t.Fatal("unreferenced old disk was not collected", removed, err)
	}
	for _, name := range []string{old.Content.SHA256 + ".img", old.Content.SHA256 + ".json"} {
		if _, err := os.Lstat(filepath.Join(store.root.Name(), "agents", name)); !os.IsNotExist(err) {
			t.Fatal("old data/metadata remain", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(store.root.Name(), "agents", old.Content.SHA256+".lock")); err != nil {
		t.Fatal("normal GC retired a reusable native lease generation", err)
	}
	if file, lease, err := store.OpenAgentsV1(t.Context(), newest.Content); err != nil {
		t.Fatal("newest disappeared", err)
	} else {
		file.Close()
		lease.Close()
	}
}

func TestAgentsGCUnknownReferencesAndFutureNewestDeleteNothing(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	build, _ := agentsStoreBuilder(t, "installed executable")
	disk, lease, err := store.EnsureAgentsV1(t.Context(), runtime.GOARCH, build)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	failure := context.Canceled
	if _, err := store.PruneAgentsV1(t.Context(), func() (map[string]string, error) { return nil, failure }); err != failure {
		t.Fatal("unknown reference authority became empty", err)
	}
	name := filepath.Join(store.root.Name(), "agents", "source.json")
	before, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	future := append([]byte(`{"future":true,`), before[1:]...)
	if err := os.WriteFile(name, future, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneAgentsV1(t.Context(), func() (map[string]string, error) { return map[string]string{}, nil }); err == nil {
		t.Fatal("future newest pointer was ignored")
	}
	if _, err := os.Lstat(filepath.Join(store.root.Name(), "agents", disk.Content.SHA256+".img")); err != nil {
		t.Fatal("future metadata permitted data deletion", err)
	}
}
