package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// stopIdentityFixture records a benign test-owned child as though a stale
// manifest PID had been reused. The real holder's kernel lock is separate.
func stopIdentityFixture(t *testing.T, boot string) (*Manifest, *os.File, *os.Process, []byte) {
	t.Helper()
	secretHome(t)
	fakeProc(t, map[string]string{"self/cgroup": "0::" + userMgr + "app.slice/tests.service\n"})
	userManager(t, "same", "same", nil)
	old := stopWait
	stopWait = 100 * time.Millisecond
	t.Cleanup(func() { stopWait = old })
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	m := &Manifest{
		Schema: 1, ID: "abcd2345", Name: "kept", Cwd: t.TempDir(),
		Agent:  AgentInfo{Kind: "shell", Command: []string{"/bin/true"}},
		Status: Status{State: StateExited},
		Run:    Runtime{HolderPID: child.Process.Pid, BootID: boot},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	f, err := openHolderLockForLaunch(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	before, err := os.ReadFile(paths.Manifest(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	return m, f, child.Process, before
}

func checkRecordedProcessUnchanged(t *testing.T, m *Manifest, child *os.Process, before []byte) {
	t.Helper()
	if err := child.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the manifest PID was signalled: %v", err)
	}
	after, err := os.ReadFile(paths.Manifest(m.ID))
	if err != nil || !bytes.Equal(after, before) {
		t.Errorf("recovery changed the session's manifest: %v\nbefore %s\nafter %s", err, before, after)
	}
}

// Neither a boot match nor a live holder lock binds a manifest PID to that
// holder. Delete and Resume retain the exact session on every refusal.
func TestPendingHolderRefusesDeleteAndResumeWithoutSignallingRecordedPID(t *testing.T) {
	for _, operation := range []string{"delete", "resume"} {
		for _, boot := range []struct{ name, value string }{{"missing-boot", ""}, {"current-boot", BootID()}, {"earlier-boot", "earlier-boot"}} {
			t.Run(operation+"/"+boot.name, func(t *testing.T) {
				m, _, child, before := stopIdentityFixture(t, boot.value)
				started := time.Now()
				var err error
				if operation == "delete" {
					var res DeleteResult
					res, err = Delete(m.ID)
					if res.Deleted {
						t.Error("delete claimed to remove a locked session")
					}
				} else {
					_, err = Resume(api.ResumeArgs{ID: m.ID})
				}
				var pe *proto.Error
				if !errors.As(err, &pe) || pe.Code != "busy" || pe.Hint == "" {
					t.Fatalf("%s: %v, want busy with a hint", operation, err)
				}
				if elapsed := time.Since(started); elapsed < stopWait || elapsed > 3*time.Second+2*stopWait {
					t.Errorf("bounded refusal took %v", elapsed)
				}
				checkRecordedProcessUnchanged(t, m, child, before)
			})
		}
	}
}

func TestPendingHolderCanReleaseItsLockWithoutRecordedPIDSignals(t *testing.T) {
	m, lock, child, before := stopIdentityFixture(t, BootID())
	old := holderStopWaiting
	t.Cleanup(func() { holderStopWaiting = old })
	holderStopWaiting = func(id string) {
		if id == m.ID {
			_ = lock.Close()
		}
	}
	if err := stopHolder(m.ID, m); err != nil {
		t.Fatalf("the holder released its lock: %v", err)
	}
	checkRecordedProcessUnchanged(t, m, child, before)
}

func TestStaleRecordedPIDDoesNotPreventDeletingAnEndedSession(t *testing.T) {
	m, lock, child, _ := stopIdentityFixture(t, BootID())
	_ = lock.Close()
	res, err := Delete(m.ID)
	if err != nil || !res.Deleted {
		t.Fatalf("stale metadata without a holder: %+v, %v", res, err)
	}
	if err := child.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("deleting stale metadata signalled its recorded PID: %v", err)
	}
	if _, err := os.Stat(paths.SessionDir(m.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the ended session was not deleted: %v", err)
	}
}

func fakeStopManager(t *testing.T, available bool) {
	t.Helper()
	bin := t.TempDir()
	body := "#!/bin/sh\nexit 1\n"
	if available {
		body = "#!/bin/sh\nprintf 'running\\n'\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestStopHolderUsesOnlyItsCanonicalOwnedUnit(t *testing.T) {
	m, lock, child, _ := stopIdentityFixture(t, BootID())
	m.Run.Unit = UnitName(m.ID)
	fakeStopManager(t, true)
	old := systemctl
	t.Cleanup(func() { systemctl = old })
	var calls [][]string
	systemctl = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		if len(args) < 2 || args[1] != m.Run.Unit {
			t.Fatalf("addressed another unit: %q", args)
		}
		if args[0] == "stop" {
			_ = lock.Close()
			return nil, nil
		}
		if len(args) == 5 && args[3] == "ActiveState" {
			return []byte("inactive\n"), nil
		}
		return []byte("not-found\n"), nil
	}
	if err := stopHolder(m.ID, m); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"stop", m.Run.Unit}, {"show", m.Run.Unit, "-p", "ActiveState", "--value"}, {"show", m.Run.Unit, "-p", "LoadState", "--value"}}
	if !slices.EqualFunc(calls, want, slices.Equal[[]string]) {
		t.Errorf("unit stop = %q, want %q", calls, want)
	}
	if err := child.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("unit stop signalled an unrelated recorded PID: %v", err)
	}
}

func TestStopHolderKeepsForeignOrUnconfirmedUnits(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{true: "foreign-unit", false: "manager-unavailable"}[foreign], func(t *testing.T) {
			m, _, child, _ := stopIdentityFixture(t, BootID())
			m.Run.Unit = UnitName(m.ID)
			if foreign {
				m.Run.Unit = UnitName("efgh2345")
			}
			if err := m.Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(paths.Manifest(m.ID))
			if err != nil {
				t.Fatal(err)
			}
			fakeStopManager(t, foreign)
			old := systemctl
			t.Cleanup(func() { systemctl = old })
			systemctl = func(context.Context, ...string) ([]byte, error) {
				t.Fatal("a foreign or unconfirmed unit was addressed")
				return nil, errors.New("unexpected manager command")
			}
			res, err := Delete(m.ID)
			var pe *proto.Error
			if !errors.As(err, &pe) || pe.Code != "busy" || pe.Hint == "" || res.Deleted {
				t.Fatalf("unit refusal = %+v, %v, want busy without deletion", res, err)
			}
			checkRecordedProcessUnchanged(t, m, child, before)
		})
	}
}
