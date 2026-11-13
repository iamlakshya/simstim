package host_test

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSocketlessLegacyHolderBlocksResumeAndRestore(t *testing.T) {
	hosttest.New(t)
	cmd := legacyHolderProcess(t)
	m := &host.Manifest{
		Schema: 1, ID: fakeID, Name: "before", Status: host.Status{State: host.StateLost},
		Run:     host.Runtime{HolderPID: cmd.Process.Pid, BootID: host.BootID()},
		Restart: host.Restart{Policy: "resume"},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(paths.SessionDir(fakeID), "rename.unsaved")
	intent := []byte(`{"name":"after"}`)
	if err := os.WriteFile(file, intent, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.Manifest(fakeID))
	if err != nil {
		t.Fatal(err)
	}
	if results := host.Restore(true); len(results) != 0 {
		t.Fatalf("restore acted on a live legacy holder: %+v", results)
	}
	if _, err := host.Resume(api.ResumeArgs{ID: fakeID}); codeOf(err) != "busy" {
		t.Fatalf("resume of live socketless holder: %v", err)
	}
	if !host.HolderPIDAlive(cmd.Process.Pid, fakeID) {
		t.Fatal("resume stopped the old holder without a safe control path")
	}
	after, err := os.ReadFile(paths.Manifest(fakeID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("live holder manifest changed: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(got, intent) {
		t.Fatalf("live holder rename intent changed: %q, %v", got, err)
	}
}

func TestRestoreRejectsUnreadableRenameStateBeforeStart(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			hosttest.New(t)
			m := &host.Manifest{
				Schema: 1, ID: fakeID, Name: "before", Status: host.Status{State: host.StateLost},
				Run: host.Runtime{RespawnCount: 1}, Restart: host.Restart{Policy: "resume"},
			}
			if err := m.Save(); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(paths.SessionDir(fakeID), "rename.unsaved")
			var err error
			if directory {
				err = os.Mkdir(file, 0o700)
			} else {
				err = os.WriteFile(file, []byte("{"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			results := host.Restore(true)
			if len(results) != 1 || results[0].Action != "failed" {
				t.Fatalf("restore with bad rename state: %+v", results)
			}
			disk, err := host.LoadManifest(fakeID)
			if err != nil || disk.Run.RespawnCount != 1 || disk.Run.HolderPID != 0 {
				t.Fatalf("failed reconciliation started or counted a holder: %+v, %v", disk, err)
			}
		})
	}
}

func TestGuardedDeleteReconcilesLegacyFinalExit(t *testing.T) {
	hosttest.New(t)
	m := &host.Manifest{Schema: 1, ID: fakeID, Name: "before", Status: host.Status{State: host.StateWorking}, Restart: host.Restart{Policy: "resume"}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.SessionDir(fakeID), "rename.unsaved"), []byte(`{"name":"after"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := paths.Socket(fakeID)
	if err := paths.EnsurePrivateDir(filepath.Dir(sock)); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		if f, err := proto.Read(bufio.NewReader(c)); err != nil || f.Type != proto.THello {
			done <- fmt.Errorf("read holder hello: %w", err)
			return
		}
		_ = ln.Close()
		_ = os.Remove(sock)
		final, err := host.LoadManifest(fakeID)
		if err == nil {
			code := 42
			final.Status = host.Status{State: host.StateErrored, ExitCode: &code, Reason: "last command failed", Source: "process"}
			err = final.Save()
		}
		done <- err
	}()
	if res, err := host.DeleteNamed(fakeID, "before"); codeOf(err) != "session_renamed" || res.Deleted {
		t.Fatalf("guarded delete after final save: %+v, %v", res, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("legacy final save did not complete")
	}
	disk, err := host.LoadManifest(fakeID)
	if err != nil || disk.Name != "after" || disk.Status.State != host.StateErrored || disk.Status.ExitCode == nil || *disk.Status.ExitCode != 42 {
		t.Fatalf("delete reconciliation clobbered final state: %+v, %v", disk, err)
	}
	results := host.Restore(true)
	if len(results) != 1 || results[0].Action != "skipped" || results[0].Why != "the agent had ended" {
		t.Fatalf("final ended session was restored: %+v", results)
	}
}
