package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// oldSandbox makes the root of session id hold a file of a run before,
// and, when live, a holder's socket that still answers. It returns the
// test driver's sandbox of the session, and the file.
func oldSandbox(t *testing.T, id string, live bool) (*plain, string) {
	t.Helper()
	testFolders(t)
	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		t.Fatal(err)
	}
	root := paths.SandboxRoot(id)
	sock := guest.Files(root).Sock
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	mark := filepath.Join(root, "from-the-run-before")
	if err := os.WriteFile(mark, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if live {
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
	}
	sb, err := newSandbox(&host.Manifest{ID: id, Wall: &host.WallRecord{Driver: driverPlain}})
	if err != nil {
		t.Fatal(err)
	}
	p := sb.(*plain)
	t.Cleanup(func() { _ = p.Kill() })
	return p, mark
}

// untouched fails the test if the start made a guest init, or replaced
// the root of the run before.
func untouched(t *testing.T, p *plain, mark string) {
	t.Helper()
	if p.cmd != nil {
		t.Error("a guest init started")
	}
	if _, err := os.Stat(mark); err != nil {
		t.Errorf("the start replaced the root of the run before: %v", err)
	}
}

// A stop while the start waits for the holder of the run before ends the
// start at once, with an error: no guest starts, and the root stays.
func TestAStopEndsTheWaitForTheRunBefore(t *testing.T) {
	p, mark := oldSandbox(t, "wait2345", true)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	conn, err := p.Start(ctx)
	if err == nil {
		conn.Close()
		t.Fatal("a start that a stop ended worked")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the start took %s after the stop", took)
	}
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "wall_setup" || !strings.Contains(pe.Message, "the start ended") {
		t.Errorf("err = %v, want the stop's", err)
	}
	untouched(t, p, mark)
}

// A holder of the run before that still answers after oldWait fails the
// start, with a hint: its root is never replaced under it.
func TestAStartFailsWhileTheRunBeforeAnswers(t *testing.T) {
	old := oldWait
	oldWait = 500 * time.Millisecond
	t.Cleanup(func() { oldWait = old })
	p, mark := oldSandbox(t, "live2345", true)
	conn, err := p.Start(context.Background())
	if err == nil {
		conn.Close()
		t.Fatal("a start with the run before still there worked")
	}
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "wall_setup" || pe.Hint == "" {
		t.Errorf("err = %#v, want wall_setup with a hint", err)
	}
	untouched(t, p, mark)
}

// A stop that comes before the start, or inside it, just before the guest
// init would start, starts no guest. Before the start, the root stays too.
func TestAStopInsideTheStartStartsNoGuest(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		p, mark := oldSandbox(t, "stop2345", false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if conn, err := p.Start(ctx); err == nil {
			conn.Close()
			t.Fatal("a start after a stop worked")
		}
		untouched(t, p, mark)
	})
	t.Run("inside", func(t *testing.T) {
		p, _ := oldSandbox(t, "stop3456", false)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		old := beforeInit
		beforeInit = cancel
		t.Cleanup(func() { beforeInit = old })
		if conn, err := p.Start(ctx); err == nil {
			conn.Close()
			t.Fatal("a start that a stop ended worked")
		}
		if p.cmd != nil {
			t.Error("a guest init started after the stop")
		}
	})
}
