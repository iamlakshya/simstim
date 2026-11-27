//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestLazyRestoreNeedsActualNativeVacancyBeforePublishing(t *testing.T) {
	for _, operation := range []*SessionOperation{nil, {}, {ownerContext: context.Background()}} {
		if err := stopLazyRestoreV1(operation); err == nil {
			t.Fatal("an absent original native lifetime published stopped")
		}
	}
}

func TestLazyRestoreWriterPreservesDiskAndRefusesChangedSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	saved := autoRestoreFixture("")
	saved.Guest = &GuestClaims{State: StateIdle}
	saved.Armed = true
	raw, err := saved.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	disk := []byte("retained workspace and agent state")
	if err := os.WriteFile(filepath.Join(dir, "disk.img"), disk, 0o600); err != nil {
		t.Fatal(err)
	}
	before := CloneManifest(*saved)
	if err := writeLazyStoppedV1(root, saved, time.Now(), func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled publication did not preserve cancellation", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err != nil || string(got) != string(raw) {
		t.Fatal("cancelled publication changed the record", err)
	}
	if err := writeLazyStoppedV1(root, saved, time.Now(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := LoadManifestFile(filepath.Join(dir, "manifest.json"))
	if err != nil || got.Status.State != StateStopped || got.Life.Phase != LifeStopped || got.Armed || !reflect.DeepEqual(*saved, before) {
		t.Fatal("lazy stop lost lifecycle or mutated its source", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "disk.img")); err != nil || string(got) != string(disk) {
		t.Fatal("lazy stop changed retained disk bytes", err)
	}
	if err := writeLazyStoppedV1(root, saved, time.Now(), func() error { return nil }); err == nil {
		t.Fatal("a stale original record replaced the stopped record")
	}
}
