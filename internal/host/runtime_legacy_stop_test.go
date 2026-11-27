package host_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestResumeWaitsForLegacyFinalSaveAfterStopReply(t *testing.T) {
	hosttest.New(t)
	cmd := legacyHolderProcess(t)
	m := &host.Manifest{
		Schema: 1, ID: fakeID, Name: "before", Cwd: t.TempDir(),
		Agent: host.AgentInfo{Kind: "shell", Command: []string{"/bin/sh", "-c", "exec sleep 60"}},
		Term:  host.TermInfo{Cols: 80, Rows: 24}, Status: host.Status{State: host.StateExited},
		Run: host.Runtime{HolderPID: cmd.Process.Pid, BootID: host.BootID()},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.SessionDir(fakeID), "rename.unsaved"), []byte(`{"name":"after"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	finish, done := make(chan struct{}), make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	go func() {
		<-finish
		final, err := host.LoadManifest(fakeID)
		if err == nil {
			final.Status.Reason = "legacy stop finished"
			err = final.Save()
		}
		_ = cmd.Process.Kill()
		if werr := cmd.Wait(); err == nil && werr != nil && cmd.ProcessState == nil {
			err = werr
		}
		done <- err
	}()
	t.Cleanup(func() {
		release()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("legacy holder cleanup did not finish")
		}
	})
	oldHook := *host.HolderStopWaiting
	t.Cleanup(func() { *host.HolderStopWaiting = oldHook })
	*host.HolderStopWaiting = func(id string) {
		if id == fakeID {
			release()
		}
	}
	var closeSocket func()
	ln := serveClosableLegacyHolder(t, func(_ int, req proto.Request) *proto.Response {
		if req.Op == "kill" {
			closeSocket()
		}
		return ok
	})
	closeSocket = func() {
		_ = ln.Close()
		_ = os.Remove(paths.Socket(fakeID))
	}
	res, err := host.Resume(api.ResumeArgs{ID: fakeID})
	if err != nil || res.Session.Name != "after" {
		t.Fatalf("acknowledged legacy stop did not resume: %+v, %v", res, err)
	}
}
