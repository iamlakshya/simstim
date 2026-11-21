//go:build linux

package slotlaunch

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func startAttemptFixtureV1(t *testing.T) (*nativeStartAttemptV1, func()) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestCheckpointNativeWorkerFixtureV1$")
	command.Env = append(os.Environ(), brand.Env("CHECKPOINT_NATIVE_WORKER_FIXTURE")+"=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	attempt := newNativeStartAttemptV1(command)
	if err := attempt.start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	finished := false
	finish := func() {
		if finished {
			return
		}
		finished = true
		input.Close()
		if err := attempt.waitCreator(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() {
		finish()
		if attempt.creator != nil {
			attempt.creator.Close()
		}
	})
	return attempt, finish
}

func TestSourceClosedFlagIsNotPhysicalEvidenceAndCloseErrorStaysSticky(t *testing.T) {
	want := errors.New("actual descriptor close failed")
	source := &NativeSourceV1{closed: true, closeErr: want}
	if source.PhysicalExitV1() {
		t.Fatal("a closed flag invented native whole-allocation closure")
	}
	if !errors.Is(source.Close(), want) || !errors.Is(source.Close(), want) {
		t.Fatal("a later Close erased the original actual close error")
	}
}

func TestIncompleteActualAttemptRetainsCreatorAndPartialRights(t *testing.T) {
	attempt, finish := startAttemptFixtureV1(t)
	partial, err := os.CreateTemp(t.TempDir(), "actual-partial-role")
	if err != nil {
		t.Fatal(err)
	}
	defer partial.Close()
	source := &NativeSourceV1{owner: t.Context(), attempt: attempt, files: []*os.File{partial}}
	if source.PhysicalExitV1() {
		t.Fatal("an incomplete native attempt invented whole-allocation closure")
	}
	if err := source.SameSourceV1(t.Context(), Request{}); err == nil {
		t.Fatal("an incomplete actual attempt became a live Source")
	}
	finish()
	if !pidExited(attempt.creator) {
		t.Fatal("the actual ordinary creator did not exit")
	}
	if source.PhysicalExitV1() {
		t.Fatal("creator exit was substituted for unknown keeper/cgroup closure")
	}
	if err := source.Wait(); err == nil {
		t.Fatal("an unknown whole allocation reported a completed native wait")
	}
	if err := source.Close(); err == nil {
		t.Fatal("an incomplete source discarded its actual retained resources")
	}
	if _, err := partial.Stat(); err != nil {
		t.Fatal("the unknown original partial vector was closed", err)
	}
	if _, err := attempt.creator.Stat(); err != nil {
		t.Fatal("the unknown original creator handle was closed", err)
	}
}

func TestUnlinkedOrdinaryDirectoryCannotProveNativeCgroupVacancy(t *testing.T) {
	attempt, finish := startAttemptFixtureV1(t)
	finish()
	dir := t.TempDir() + "/ordinary"
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	group, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	info, err := group.Stat()
	if err != nil {
		t.Fatal(err)
	}
	native := info.Sys().(*syscall.Stat_t)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	files := make([]*os.File, 13)
	files[0], files[1], files[2] = attempt.creator, attempt.creator, group
	// This is an explicit data-forgery falsifier, not a Source issuer. The
	// actual filesystem/type/inode guard must reject its ordinary directory.
	source := &NativeSourceV1{adopted: true, files: files, parent: parent, identity: cgroupIdentity{Dev: uint64(native.Dev), Ino: native.Ino}}
	if source.PhysicalExitV1() {
		t.Fatal("an unlinked non-cgroup inode was accepted as whole native vacancy")
	}
}
