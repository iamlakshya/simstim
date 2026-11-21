//go:build linux

package host_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// This is a real outside bridge process. Closing it joins its actual native
// process before the next observer acts; no saved PID stands in for that join.
func newNoneProcessBridgeV1(t *testing.T) (*rawBridge, func()) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	b := &rawBridge{t: t, w: proto.NewWriter(inW), frames: make(chan proto.Frame, 1024), in: inW, done: make(chan error, 1)}
	command := exec.Command(executable, "bridge")
	command.Stdin = inR
	command.Stdout = outW
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = command.Wait()
		_ = outW.CloseWithError(io.EOF)
		_ = inR.Close()
		b.done <- waitErr
		close(done)
	}()
	go func() {
		reader := bufio.NewReader(outR)
		defer outR.Close()
		defer close(b.frames)
		for {
			frame, err := proto.Read(reader)
			if err != nil {
				return
			}
			b.frames <- frame
		}
	}()
	var once sync.Once
	finish := func() {
		once.Do(func() {
			_ = inW.Close()
			select {
			case <-done:
				if waitErr != nil {
					t.Error(waitErr)
				}
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Error("the owned outside Noe bridge did not join")
			}
		})
	}
	t.Cleanup(finish)
	b.sendJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "client"})
	_ = b.next(proto.THello)
	return b, finish
}

func stopNoneInheritedFixtureV1(t *testing.T, id string) {
	t.Helper()
	op, err := host.BeginSessionOperation(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	stopped, err := host.StopNoneSessionForOperationV1(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	defer stopped.Close()
	if err := stopped.Check(context.Background(), op); err != nil {
		t.Fatal(err)
	}
}

func TestNoneOriginalBridgeCanExitBeforeStopColdResumeAndRepeat(t *testing.T) {
	requireManagerlessNoneFixtureV1(t)
	hosttest.New(t)
	first, firstExit := newNoneProcessBridgeV1(t)
	created := first.request(80, "new", host.NewArgs{Wall: "none", Name: "inherited-none", Cwd: "~", Argv: []string{"/bin/sh", "-c", "printf INHERITED-NONE; exec sleep 60"}, Cols: 80, Rows: 24})
	if !created.OK {
		t.Fatal(created.Error)
	}
	var session host.SessionInfo
	if err := json.Unmarshal(created.Result, &session); err != nil || session.ID == "" {
		t.Fatal("the genuine bridge returned no created Noe session", err)
	}
	firstExit() // Historical flock locker is gone; inherited SAME OFD remains.
	waitOutput(t, session.ID, "INHERITED-NONE")
	stopNoneInheritedFixtureV1(t, session.ID)
	second, secondExit := newNoneProcessBridgeV1(t)
	prepared := second.request(81, "resume.prepare-v1", api.ResumePrepareArgsV1{V: 1, Start: api.ResumeArgs{ID: session.ID}})
	if !prepared.OK {
		t.Fatal(prepared.Error)
	}
	var plan api.ResumePrepareResultV1
	if err := json.Unmarshal(prepared.Result, &plan); err != nil {
		t.Fatal(err)
	}
	if response := second.request(82, "resume", api.ResumeArgs{ID: session.ID, Prepared: plan.Prepared}); !response.OK {
		t.Fatal(response.Error)
	}
	secondExit()
	waitOutput(t, session.ID, "INHERITED-NONE")
	stopNoneInheritedFixtureV1(t, session.ID)
	// All native joins above were positively checked. Remove only the
	// fixture's own already-stopped temporary metadata, without a DTO issuer.
	if err := os.RemoveAll(paths.SessionDir(session.ID)); err != nil {
		t.Fatal(err)
	}
}
