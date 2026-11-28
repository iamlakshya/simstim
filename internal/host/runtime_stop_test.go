package host_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestStopRestoreResumeAndDestroy(t *testing.T) {
	hosttest.New(t)
	work := t.TempDir()
	marker := filepath.Join(work, "runs")
	s, err := host.New(host.NewArgs{
		Name: "life",
		Cwd:  work,
		Argv: []string{"/bin/sh", "-c", "echo run >> " + marker + "; exec sleep 60"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hosttest.WaitFor(t, 5*time.Second, "initial run marker", func() bool {
		b, err := os.ReadFile(marker)
		return err == nil && strings.Count(string(b), "run") >= 1
	})

	// Stop tears down the holder and sockets while keeping the session folder
	// and manifest in StateStopped.
	stopped, err := host.Stop(api.StopArgs{ID: s.ID})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.AlreadyStopped {
		t.Error("first Stop reported AlreadyStopped")
	}
	if stopped.Session.State != host.StateStopped || stopped.Session.Alive || stopped.Session.Unit != "" {
		t.Fatalf("after Stop: %+v", stopped.Session)
	}
	if _, err := os.Stat(paths.Socket(s.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("holder socket still exists: %v", err)
	}
	if _, err := os.Stat(paths.HookSocket(s.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("hook socket still exists: %v", err)
	}
	if _, err := os.Stat(paths.Manifest(s.ID)); err != nil {
		t.Fatalf("manifest missing after Stop: %v", err)
	}

	// List preserves StateStopped (never turns it into StateLost).
	list, err := host.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != host.StateStopped || list[0].Alive {
		t.Fatalf("List after Stop = %+v", list)
	}

	// Stopping again is idempotent.
	again, err := host.Stop(api.StopArgs{ID: s.ID})
	if err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if !again.AlreadyStopped || again.Session.State != host.StateStopped {
		t.Fatalf("second Stop = %+v", again)
	}

	// Boot-time Restore(false) skips explicitly stopped sessions.
	bootRes := host.Restore(false)
	if len(bootRes) != 1 || bootRes[0].Action != "skipped" || !strings.Contains(bootRes[0].Why, "stopped") {
		t.Fatalf("Restore(false) = %+v", bootRes)
	}

	// Restore(true) brings a stopped session back from its kept state.
	allRes := host.Restore(true)
	if len(allRes) != 1 || allRes[0].Action != "restored" {
		t.Fatalf("Restore(true) = %+v", allRes)
	}
	hosttest.WaitFor(t, 5*time.Second, "restored run marker", func() bool {
		b, err := os.ReadFile(marker)
		return err == nil && strings.Count(string(b), "run") >= 2
	})
	list, err = host.List()
	if err != nil || len(list) != 1 || !list[0].Alive || list[0].State == host.StateStopped {
		t.Fatalf("List after Restore(true) = %+v, err=%v", list, err)
	}

	// Stop again, then Resume brings it back as well.
	if _, err := host.Stop(api.StopArgs{ID: s.ID}); err != nil {
		t.Fatalf("Stop before Resume: %v", err)
	}
	resumed, err := host.Resume(api.ResumeArgs{ID: s.ID})
	if err != nil {
		t.Fatalf("Resume after Stop: %v", err)
	}
	if !resumed.Session.Alive || resumed.Session.State == host.StateStopped {
		t.Fatalf("after Resume: %+v", resumed.Session)
	}
	hosttest.WaitFor(t, 5*time.Second, "resumed run marker", func() bool {
		b, err := os.ReadFile(marker)
		return err == nil && strings.Count(string(b), "run") >= 3
	})

	// Destroy stops the session and removes its session folder completely.
	del, err := host.Delete(s.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !del.Deleted {
		t.Fatal("Delete reported Deleted=false")
	}
	if _, err := os.Stat(paths.SessionDir(s.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("session folder still exists after Delete: %v", err)
	}
	if _, err := os.Stat(paths.Socket(s.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still exists after Delete: %v", err)
	}
	list, err = host.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("List after Delete = %+v, err=%v", list, err)
	}
}

func TestStopRefusedInsideSessionAndMissingID(t *testing.T) {
	hosttest.New(t)
	s := start(t, "inside", "sleep", "60")

	if _, err := host.Stop(api.StopArgs{ID: "nosuch99"}); err == nil {
		t.Fatal("Stop of missing ID succeeded")
	} else {
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != "not_found" {
			t.Fatalf("missing ID err = %v, want not_found", err)
		}
	}

	hosttest.InSession(t, s.ID)
	if _, err := host.Stop(api.StopArgs{ID: s.ID}); err == nil {
		t.Fatal("Stop from inside session succeeded")
	} else {
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != "forbidden" {
			t.Fatalf("inside session err = %v, want forbidden", err)
		}
	}
}
