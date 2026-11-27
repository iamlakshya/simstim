package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStateFDLockChild(t *testing.T) {
	mode := os.Getenv("STATE_FD_CHILD")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	err := withStateFileFD(ctx, nil, "facts.json", false, func(view stateFileView) (stateFileReplace, error) {
		if !view.Exists {
			t.Fatal("child snapshot missing")
		}
		if err := os.WriteFile(filepath.Join(StateDir(), "reader-ready"), nil, 0600); err != nil {
			return nil, err
		}
		for {
			if _, err := os.Stat(filepath.Join(StateDir(), "reader-release")); err == nil {
				return nil, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
	if mode == "replaced" {
		if err == nil {
			t.Fatal("child accepted its replaced lock domain")
		}
	} else if err != nil {
		t.Fatal(err)
	}
}

func TestStateFDReaderAndWriterShareCrossProcessDomain(t *testing.T) {
	for _, kind := range []string{"release", "cancel", "replace-lock"} {
		t.Run(kind, func(t *testing.T) {
			stateFDTestHome(t)
			stateFDWrite(t, nil, "facts.json", []byte("old"))
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			childCtx, stop := context.WithTimeout(t.Context(), 20*time.Second)
			defer stop()
			cmd := exec.CommandContext(childCtx, exe, "-test.run=^TestStateFDLockChild$")
			mode := "held"
			if kind == "replace-lock" {
				mode = "replaced"
			}
			cmd.Env = append(os.Environ(), "STATE_FD_CHILD="+mode)
			var output lockedWriter
			cmd.Stdout, cmd.Stderr = &output, &output
			cmd.WaitDelay = 2 * time.Second
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			childDone := make(chan error, 1)
			go func() { childDone <- cmd.Wait() }()
			t.Cleanup(func() {
				stop()
				select {
				case <-childDone:
				case <-time.After(5 * time.Second):
					t.Error("owned child did not exit")
				}
			})
			for until := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(filepath.Join(StateDir(), "reader-ready")); err == nil {
					break
				}
				if time.Now().After(until) {
					t.Fatalf("child did not hold canonical snapshot: %s", output.String())
				}
			}
			waiting := make(chan struct{})
			var once sync.Once
			observed := context.WithValue(t.Context(), stateFileWaitKey{}, func() { once.Do(func() { close(waiting) }) })
			parentCtx, cancel := context.WithCancel(observed)
			called := false
			result := make(chan error, 1)
			parentDone := make(chan struct{})
			defer func() {
				cancel()
				select {
				case <-parentDone:
				case <-time.After(5 * time.Second):
					t.Error("parent state transaction did not stop")
				}
			}()
			go func() {
				defer close(parentDone)
				result <- withStateFileFD(parentCtx, nil, "facts.json", true, func(stateFileView) (stateFileReplace, error) { called = true; return nil, nil })
			}()
			select {
			case <-waiting:
			case <-time.After(5 * time.Second):
				t.Fatal("parent did not wait in actual file-lock domain")
			}
			if kind == "cancel" {
				cancel()
			}
			if kind == "replace-lock" {
				path := filepath.Join(StateDir(), "facts.json.lock")
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("foreign domain"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(StateDir(), "reader-release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if kind == "release" && (err != nil || !called) {
					t.Fatalf("released shared domain did not enter: called=%v error=%v", called, err)
				}
				if kind == "cancel" && (!errors.Is(err, context.Canceled) || called) {
					t.Fatalf("cancelled waiter entered: called=%v error=%v", called, err)
				}
				if kind == "replace-lock" && (err == nil || called) {
					t.Fatalf("stale waiter adopted replacement: called=%v error=%v", called, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("parent did not leave canonical lock wait")
			}
			select {
			case err := <-childDone:
				if err != nil {
					t.Fatalf("owned child: %v\n%s", err, output.String())
				}
				childDone <- nil
			case <-time.After(5 * time.Second):
				t.Fatal("child snapshot did not finish")
			}
		})
	}
}
