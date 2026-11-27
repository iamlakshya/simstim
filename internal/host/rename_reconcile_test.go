package host

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

func TestReconcileRenameReloadsFinalManifest(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled", true: "uncertain"}[uncertain], func(t *testing.T) {
			checkReconcileFinalManifest(t, uncertain)
		})
	}
}

func TestReconcileWriteFailureKeepsCallerAndIntent(t *testing.T) {
	if os.Getenv(brand.Env("TEST_RECONCILE_SIZE")) != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReconcileWriteFailureKeepsCallerAndIntent$", "-test.count=1")
		cmd.Env = append(os.Environ(), brand.Env("TEST_RECONCILE_SIZE")+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated file-limit regression: %v\n%s", err, out)
		}
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	m := &Manifest{Schema: 1, ID: "abcd2345", Name: "before"}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if err := writeUnsaved(m.ID, unsavedRename{Name: "after"}); err != nil {
		t.Fatal(err)
	}
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	limited := original
	limited.Cur = 64
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	err := reconcileUnsavedLocked(m)
	if restoreErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err == nil || m.Name != "before" {
		t.Fatalf("failed save changed caller or succeeded: name=%q, err=%v", m.Name, err)
	}
	if _, err := os.Stat(unsavedPath(m.ID)); err != nil {
		t.Fatalf("failed save removed rename intent: %v", err)
	}
	if err := reconcileUnsavedLocked(m); err != nil {
		t.Fatal(err)
	}
	disk, err := LoadManifest(m.ID)
	if err != nil || disk.Name != "after" {
		t.Fatalf("retry lost settled name: %+v, %v", disk, err)
	}
}

func checkReconcileFinalManifest(t *testing.T, uncertain bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	run, err := testhome.ShortTempDir("rn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(run) })
	t.Setenv("XDG_RUNTIME_DIR", run)
	const id = "abcd2345"
	m := &Manifest{Schema: 1, ID: id, Name: "before", Status: Status{State: StateWorking}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	stale, err := LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	code := 42
	m.Status = Status{State: StateErrored, Reason: "the command failed", ExitCode: &code, Source: "process"}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if err := writeUnsaved(id, unsavedRename{Name: "after", Uncertain: uncertain}); err != nil {
		t.Fatal(err)
	}
	if err := reconcileUnsavedLocked(stale); err != nil {
		t.Fatal(err)
	}
	disk, err := LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	wantName := "after"
	if uncertain {
		wantName = "before"
	}
	for _, got := range []*Manifest{stale, disk} {
		if got.Name != wantName || got.Status.State != StateErrored || got.Status.ExitCode == nil || *got.Status.ExitCode != 42 || got.Status.Reason != "the command failed" {
			t.Fatalf("reconciliation lost final state: %+v", got)
		}
	}
	_, err = os.Stat(unsavedPath(id))
	if uncertain && err != nil {
		t.Fatalf("uncertain rename state removed: %v", err)
	}
	if !uncertain && !os.IsNotExist(err) {
		t.Fatalf("settled rename remains: %v", err)
	}
	results := Restore(true)
	if len(results) != 1 || results[0].Action != "skipped" || results[0].Why != "the agent had ended" {
		t.Fatalf("restore of reconciled final state = %+v", results)
	}
	if _, err := os.Stat(paths.Manifest(id)); err != nil {
		t.Fatal(err)
	}
}
