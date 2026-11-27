package client

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestOwnedStageNeverAdoptsAReplacement(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	ctxA, abortA, err := StagePinOwned(context.Background(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abortA()
	_, abortB, err := StagePinOwned(context.Background(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abortB()
	want := currentStage(h.Name)
	for _, op := range []struct {
		name string
		run  func() error
	}{
		{"pin", func() error { _, err := PinHostKey(ctxA, h, "", FirstUseAccept); return err }},
		{"command", func() error { _, err := (&SSH{Host: h}).Command(ctxA, "true"); return err }},
		{"need", func() error { _, err := needsPin(ctxA, h); return err }},
		{"kept", func() error { _, err := keptPin(ctxA, h.Name, ""); return err }},
		{"save", func() error { return SaveHostPinContext(ctxA, h) }},
	} {
		t.Run(op.name, func(t *testing.T) {
			var refusal *UserError
			if err := op.run(); !errors.As(err, &refusal) || refusal.Kind != KindCancelled {
				t.Fatalf("superseded operation = %v", err)
			}
			if currentStage(h.Name) != want {
				t.Fatal("old operation changed replacement")
			}
			if _, err := os.Stat(want.path); err != nil {
				t.Fatal(err)
			}
			if HasPin(h.Name) {
				t.Fatal("old operation published a pin")
			}
		})
	}
	abortA()
	abortA()
	if currentStage(h.Name) != want {
		t.Fatal("old abort removed replacement")
	}
}

func TestOwnedStageBindingsArePerHost(t *testing.T) {
	sshDirs(t)
	ctx, abortA, err := StagePinOwned(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer abortA()
	a := currentStage("alpha")
	ctx, abortB, err := StagePinOwned(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	defer abortB()
	if got, err := stageForContext(ctx, "alpha"); err != nil || got != a {
		t.Fatalf("alpha binding = %p, %v", got, err)
	}
	if got, err := stageForContext(ctx, "beta"); err != nil || got != currentStage("beta") {
		t.Fatalf("beta binding = %p, %v", got, err)
	}
}

func TestOwnedPinSaveRollsBackWhenHostsLockIsCancelled(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "restore"}[previous], func(t *testing.T) {
			sshDirs(t)
			h := Host{Name: "box", SSH: "box.example"}
			var before []byte
			if previous {
				if err := writePin(h.Name, nil, []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = os.ReadFile(pinPath(h.Name))
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, abort, err := StagePinOwned(ctx, h.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer abort()
			st := currentStage(h.Name)
			if err := writePin(h.Name, st, []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
				t.Fatal(err)
			}
			unlock, err := lockHostsFile(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			old := hostsFileWaiting
			defer func() { hostsFileWaiting = old }()
			waiting := make(chan struct{})
			var once sync.Once
			hostsFileWaiting = func() { once.Do(func() { close(waiting) }) }
			done := make(chan error, 1)
			go func() { done <- SaveHostPinContext(ctx, h) }()
			select {
			case <-waiting:
			case <-time.After(2 * time.Second):
				t.Fatal("save did not wait for hosts lock")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("save = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled save stayed blocked")
			}
			got, readErr := os.ReadFile(pinPath(h.Name))
			if previous {
				if readErr != nil || string(got) != string(before) {
					t.Fatalf("previous pin was not restored: %v", readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("new pin remains: %v", readErr)
			}
			if currentStage(h.Name) != nil {
				t.Fatal("cancelled save left a stage")
			}
			if _, err := os.Stat(st.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("staged file remains: %v", err)
			}
		})
	}
}

func TestStagedCommandsDoNotLeaveReusableMasters(t *testing.T) {
	fakeSSH(t)
	h := Host{Name: "stage", SSH: "stage.example"}
	ctx, abort, err := StagePinOwned(context.Background(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	if err := writeTestPin(h.Name, []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	for _, shell := range []bool{false, true} {
		var args []string
		if shell {
			cmd, err := (&SSH{Host: h}).Shell(ctx)
			if err != nil {
				t.Fatal(err)
			}
			args = cmd.Args
		} else {
			cmd, err := (&SSH{Host: h}).Command(ctx, "true")
			if err != nil {
				t.Fatal(err)
			}
			args = cmd.Args
		}
		if !hasRun(args, OwnConnection()) {
			t.Fatalf("staged command can reuse a master: %q", args)
		}
		if !hasRun(args, []string{"-o", "StrictHostKeyChecking=yes"}) {
			t.Fatalf("stage lost its pin guard: %q", args)
		}
	}
	if err := SaveHostPinContext(ctx, h); err != nil {
		t.Fatal(err)
	}
	cmd, err := (&SSH{Host: h}).Command(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	if controlMaster && sharedPath(cmd.Args) == "none" {
		t.Fatal("saved host did not regain sharing")
	}
}

func TestOwnedAbortRemovesItsClosedStageAfterLockTimeout(t *testing.T) {
	sshDirs(t)
	_, abort, err := StagePinOwned(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	st := currentStage("box")
	unlock, err := lockPinWait("box")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	old := stageAbortWait
	stageAbortWait = 20 * time.Millisecond
	t.Cleanup(func() { stageAbortWait = old })
	abort()
	if currentStage("box") != nil {
		t.Fatal("timed-out abort left the closed stage attached")
	}
	if _, err := os.Stat(st.path); !os.IsNotExist(err) {
		t.Fatalf("closed stage leaked: %v", err)
	}
	if err := writePin("box", st, []hostKey{newKey(t, "ssh-ed25519")}); err == nil {
		t.Fatal("closed stage wrote after abort")
	}
}
