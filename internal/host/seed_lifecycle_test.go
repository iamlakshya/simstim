//go:build linux || darwin

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func seedStopFlag(t *testing.T, stopped bool) {
	t.Helper()
	liveGitOpsMu.Lock()
	previous := gitOpsStopped
	gitOpsStopped = stopped
	liveGitOpsMu.Unlock()
	t.Cleanup(func() { liveGitOpsMu.Lock(); gitOpsStopped = previous; liveGitOpsMu.Unlock() })
}

type cancelCopyReader struct {
	reads  int
	cancel context.CancelFunc
}

func (r *cancelCopyReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 2 {
		r.cancel()
	}
	if r.reads > 2 {
		return 0, io.EOF
	}
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestSeedCopyHonoursCancellationBeforeAndDuringInclude(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := &cancelCopyReader{cancel: cancel}
	var output bytes.Buffer
	if n, err := seedCopy(ctx, &output, before); n != 0 || !errors.Is(err, context.Canceled) || before.reads != 0 {
		t.Fatalf("copy began after disconnect: %d %v", n, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	during := &cancelCopyReader{cancel: cancel}
	n, err := seedCopy(ctx, &output, during)
	if !errors.Is(err, context.Canceled) || n != 32<<10 || output.Len() != 32<<10 {
		t.Fatalf("copy continued after disconnect: %d %d %v", n, output.Len(), err)
	}
}

func TestSeedCannotFollowOnAfterConnectionStops(t *testing.T) {
	seedStopFlag(t, true)
	if _, err := createSeed(NewArgs{}, "stopped2", "fixture", t.TempDir()); err == nil {
		t.Fatal("seed began after disconnect")
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if ended, err := liveGitOp("seed", cancel); err == nil {
		ended()
		t.Fatal("post-disconnect Git launch was admitted")
	}
	if err := seedConnectionAlive(); err == nil {
		t.Fatal("post-copy publication was admitted after disconnect")
	}
}

func launchFixture(t *testing.T) (*Manifest, *sessionReservation) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	reservation, err := reserveSessionID("launchab")
	if err != nil {
		t.Fatal(err)
	}
	return &Manifest{Schema: 1, ID: reservation.id, Name: "fixture", Files: &FilePlan{}}, reservation
}

func TestDisconnectBeforeLaunchRemovesOnlyUnlaunchedSeed(t *testing.T) {
	seedStopFlag(t, true)
	manifest, reservation := launchFixture(t)
	previous := startHolder
	t.Cleanup(func() { startHolder = previous })
	startHolder = func(m *Manifest) (*os.Process, error) {
		command := exec.Command("/usr/bin/true")
		err := startSessionCommand(m, command)
		if err == nil {
			_ = command.Wait()
			return nil, UserError("start_failed", "fixture launch unexpectedly ran", "Inspect the launch guard.")
		}
		return nil, err
	}
	_, err := startReservedSession(manifest, nil, reservation)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "git_op" || reservation.launched {
		t.Fatalf("unlaunched seed became an owned session: %v", err)
	}
	reservation.close()
	if _, err := os.Stat(paths.SessionDir(manifest.ID)); !os.IsNotExist(err) {
		t.Fatal("unlaunched seed was kept", err)
	}
}

func TestLostReplyAfterActualLaunchKeepsOwnedSessionFiles(t *testing.T) {
	seedStopFlag(t, false)
	manifest, reservation := launchFixture(t)
	previous := startHolder
	t.Cleanup(func() { startHolder = previous })
	startHolder = func(m *Manifest) (*os.Process, error) {
		cmd := exec.Command("/usr/bin/true")
		if err := startSessionCommand(m, cmd); err != nil {
			return nil, err
		}
		if err := cmd.Wait(); err != nil {
			return nil, err
		}
		liveGitOpsMu.Lock()
		gitOpsStopped = true
		liveGitOpsMu.Unlock()
		return nil, UserError("start_failed", "fixture lost its reply", "List sessions to recover it.")
	}
	_, err := startReservedSession(manifest, nil, reservation)
	if err == nil || !reservation.launched || !manifest.launchStarted {
		t.Fatalf("actual launch was treated as seed scratch: %v", err)
	}
	reservation.close()
	if _, err := os.Stat(filepath.Join(paths.SessionDir(manifest.ID), "manifest.json")); err != nil {
		t.Fatal("lost-reply session was deleted", err)
	}
}
