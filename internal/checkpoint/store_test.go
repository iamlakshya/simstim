package checkpoint

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

type fixture struct {
	store    *Store
	session  *os.Root
	vm       *os.Root
	disk     *os.File
	stopped  bool
	stopErr  error
	stopSeen int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		name := filepath.Join(dir, key)
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, name)
	}
	if err := os.Mkdir(filepath.Join(dir, "vm"), 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := cachefs.OpenPrivateChild(session, "vm", false)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := cachefs.OpenFile(vm, diskName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{session: session, vm: vm, disk: disk}
	f.store, err = OpenOwned(t.Context(), session, vm, disk, func(ctx context.Context) error {
		if f.stopped {
			return errors.New("the original running source has stopped")
		}
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	// Test the transaction independently of a developer machine's filesystem.
	// The real FICLONE probe is exercised separately where it is available.
	f.store.clone = func(target, source *os.File) error {
		info, err := source.Stat()
		if err != nil {
			return err
		}
		_, err = io.Copy(target, io.NewSectionReader(source, 0, info.Size()))
		return err
	}
	t.Cleanup(func() {
		_ = f.store.Close()
		_ = f.disk.Close()
		_ = f.vm.Close()
		_ = f.session.Close()
	})
	return f
}

func (f *fixture) checkStopped(ctx context.Context) error {
	f.stopSeen++
	if !f.stopped {
		return errors.New("the original runtime has not physically stopped")
	}
	return errors.Join(ctx.Err(), f.stopErr)
}

func (f *fixture) write(t *testing.T, value string) {
	t.Helper()
	if err := f.disk.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.disk.WriteAt([]byte(value), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.disk.Sync(); err != nil {
		t.Fatal(err)
	}
}

func metadata(id string) Metadata {
	return Metadata{AgentSessionIDs: map[string]string{"session": id}, Refs: []Ref{{Name: "refs/heads/task", SHA: strings.Repeat("a", 40)}}, Turn: "finished turn"}
}

func TestRewindPinsOldestBeforeSafetyCheckpointAndRestoresConversation(t *testing.T) {
	f := newFixture(t)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for n := range 3 {
		f.write(t, []string{"old files", "middle files", "new files"}[n])
		if _, err := f.store.Create(t.Context(), at.Add(time.Duration(n)*time.Minute), metadata([]string{"old-conversation", "middle-conversation", "new-conversation"}[n]), 3); err != nil {
			t.Fatal(err)
		}
	}
	selection, err := f.store.Resolve(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Close()
	f.stopped = true
	if _, err := f.store.CreateStopped(t.Context(), at.Add(3*time.Minute), metadata("new-conversation"), 0, f.checkStopped); err != nil {
		t.Fatal(err)
	}
	// There must still be four pairs when retention was already full.
	keys, err := f.store.keys(t.Context())
	if err != nil || len(keys) != 4 {
		t.Fatalf("safety checkpoint discarded the selected old disk: %v, %v", keys, err)
	}
	newDisk, err := f.store.Restore(t.Context(), selection, f.checkStopped)
	if err != nil {
		t.Fatal(err)
	}
	defer newDisk.Close()
	data, err := io.ReadAll(io.NewSectionReader(newDisk, 0, 100))
	if err != nil || string(data) != "old files" {
		t.Fatalf("rewind files = %q, %v", data, err)
	}
	if got := selection.Entry().Metadata.AgentSessionIDs["session"]; got != "old-conversation" {
		t.Fatalf("rewind did not retain the older conversation ID: %q", got)
	}
	if f.stopSeen < 3 {
		t.Fatal("rewind did not repeat its original native stopped proof")
	}
	if err := f.store.TrimStopped(t.Context(), 3, f.checkStopped); err != nil {
		t.Fatal(err)
	}
	keys, err = f.store.keys(t.Context())
	if err != nil || len(keys) != 3 {
		t.Fatalf("post-restore retention = %v, %v", keys, err)
	}
}

func TestRestoreRequiresOriginalStopAndFreshUnchangedSafetyDisk(t *testing.T) {
	f := newFixture(t)
	f.write(t, "checkpoint")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.store.Create(t.Context(), at, metadata("old"), 10); err != nil {
		t.Fatal(err)
	}
	selection, err := f.store.Resolve(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Close()
	if disk, err := f.store.Restore(t.Context(), selection, nil); err == nil || disk != nil {
		t.Fatal("restore accepted no native stopped proof")
	}
	if disk, err := f.store.Restore(t.Context(), selection, f.checkStopped); err == nil || disk != nil {
		t.Fatal("restore accepted a still-running original runtime")
	}
	f.stopped = true
	if disk, err := f.store.Restore(t.Context(), selection, f.checkStopped); err == nil || disk != nil {
		t.Fatal("restore accepted no new safety checkpoint")
	}
	if _, err := f.store.CreateStopped(t.Context(), at.Add(time.Minute), metadata("new"), 0, f.checkStopped); err != nil {
		t.Fatal(err)
	}
	f.write(t, "changed after safety checkpoint")
	if disk, err := f.store.Restore(t.Context(), selection, f.checkStopped); err == nil || disk != nil {
		t.Fatal("restore replaced a disk changed after its safety checkpoint")
	}
}

func TestFailedCloneCleansPartsWithoutDisablingSupportedFilesystem(t *testing.T) {
	f := newFixture(t)
	f.write(t, "current files")
	f.store.clone = func(target, _ *os.File) error {
		_, _ = target.WriteString("partial clone")
		return os.ErrPermission
	}
	_, err := f.store.Create(t.Context(), time.Now(), metadata("current"), 10)
	var wire *proto.Error
	if !errors.As(err, &wire) || wire.Code != "checkpoint" {
		t.Fatalf("transient clone failure disabled checkpoints: %v", err)
	}
	dir, err := f.store.entries.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed clone retained unfinished files: %v, %v", entries, err)
	}
}

func TestSelectedMetadataSubstitutionPreventsRestore(t *testing.T) {
	f := newFixture(t)
	f.write(t, "old files")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.store.Create(t.Context(), at, metadata("old"), 10); err != nil {
		t.Fatal(err)
	}
	selection, err := f.store.Resolve(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Close()
	f.stopped = true
	if _, err := f.store.CreateStopped(t.Context(), at.Add(time.Minute), metadata("current"), 0, f.checkStopped); err != nil {
		t.Fatal(err)
	}
	name := selection.key + ".json"
	if err := f.store.entries.Remove(name); err != nil {
		t.Fatal(err)
	}
	replacement, err := cachefs.OpenFile(f.store.entries, name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if disk, err := f.store.Restore(t.Context(), selection, f.checkStopped); err == nil || disk != nil {
		t.Fatal("rewind accepted a metadata inode substituted after selection")
	}
}

func TestRestoreReturnsActualNewDescriptorWhenPostRenameOwnerCheckFails(t *testing.T) {
	f := newFixture(t)
	f.write(t, "old files")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.store.Create(t.Context(), at, metadata("old"), 10); err != nil {
		t.Fatal(err)
	}
	selection, err := f.store.Resolve(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Close()
	f.write(t, "current files")
	f.stopped = true
	if _, err := f.store.CreateStopped(t.Context(), at.Add(time.Minute), metadata("current"), 0, f.checkStopped); err != nil {
		t.Fatal(err)
	}
	old, err := f.disk.Stat()
	if err != nil {
		t.Fatal(err)
	}
	checkStopped := func(ctx context.Context) error {
		if err := f.checkStopped(ctx); err != nil {
			return err
		}
		current, err := f.vm.Lstat(diskName)
		if err != nil {
			return err
		}
		if !os.SameFile(old, current) {
			return errors.New("the original owner requires adoption of the actual replacement")
		}
		return nil
	}
	replacement, err := f.store.Restore(t.Context(), selection, checkStopped)
	if replacement == nil || err == nil {
		t.Fatalf("post-rename failure lost its actual created disk: %v, %v", replacement, err)
	}
	defer replacement.Close()
	opened, err := cachefs.ValidateFile(f.vm, diskName, replacement, 0o600)
	if err != nil || os.SameFile(old, opened) {
		t.Fatalf("returned descriptor is not the actual new canonical disk: %v, %v", opened, err)
	}
	keys, err := f.store.keys(t.Context())
	if err != nil || len(keys) != 2 {
		t.Fatalf("uncertain final check deleted a checkpoint: %v, %v", keys, err)
	}
}

func TestCheckpointFolderReplacementDoesNotRedirectStore(t *testing.T) {
	f := newFixture(t)
	if err := f.session.Rename("checkpoints", "retained-checkpoints"); err != nil {
		t.Fatal(err)
	}
	if err := f.session.Mkdir("checkpoints", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Create(t.Context(), time.Now(), metadata("current"), 10); err == nil {
		t.Fatal("store accepted a same-name checkpoint directory replacement")
	}
}

func TestOriginalDiskHardLinkPreventsCheckpointCreation(t *testing.T) {
	f := newFixture(t)
	if err := f.vm.Link(diskName, "linked-disk"); err != nil {
		t.Skipf("this filesystem cannot create the planted hard link: %v", err)
	}
	if _, err := f.store.Create(t.Context(), time.Now(), metadata("current"), 10); err == nil {
		t.Fatal("store copied an original disk with another hard link")
	}
	if _, err := f.vm.Lstat("linked-disk"); err != nil {
		t.Fatal("store removed the planted unknown hard link")
	}
}

func TestMetadataRejectsMultipleValuesAndOversizedIDs(t *testing.T) {
	data, err := metadataBytes(metadata("old"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeMetadata(append(data, []byte(" {}")...)); err == nil {
		t.Fatal("checkpoint metadata accepted a second JSON value")
	}
	if _, err := metadataBytes(metadata(strings.Repeat("x", 1025))); err == nil {
		t.Fatal("checkpoint metadata accepted an oversized conversation ID")
	}
}
