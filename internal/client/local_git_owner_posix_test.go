//go:build linux || darwin

package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These modes exist only in this test binary. They deliberately model native
// descendants without claiming that arbitrary detached programs are contained.
func init() {
	if len(os.Args) == 2 && (os.Args[1] == "local-git-pack-fixture-v1" || os.Args[1] == "local-git-pack-after-fork-v1") {
		mode, err := readGitFixtureCase()
		if err != nil {
			return
		}
		if mode == "cancel-fork" && os.Args[1] == "local-git-pack-fixture-v1" {
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, syscall.SIGTERM)
			_ = os.WriteFile(".native-pack-ready", []byte("ready"), 0600)
			<-signals
			exe, _ := os.Executable()
			child := exec.Command(exe, "local-git-pack-after-fork-v1")
			child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				os.Exit(124)
			}
			os.Exit(0)
		}
		_ = os.WriteFile(".native-pack-ready", []byte("ready"), 0600)
		time.Sleep(15 * time.Second)
		os.Exit(0)
	}
	if filepath.Base(os.Args[0]) != "git" || os.Args[len(os.Args)-1] != "version" {
		return
	}
	mode, err := readGitFixtureCase()
	if err != nil {
		return
	}
	if err := validateGitRoleEnvironment(localGitNormal, os.Environ(), false); err != nil {
		os.Exit(121)
	}
	switch mode {
	case "residual", "cancel", "cancel-fork":
		exe, _ := os.Executable()
		child := exec.Command(exe, "local-git-pack-fixture-v1")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(124)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(".native-pack-ready"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(122)
			}
			time.Sleep(time.Millisecond)
		}
		_ = os.WriteFile(".native-started", []byte("native pack started"), 0600)
		if mode != "residual" {
			time.Sleep(15 * time.Second)
			os.Exit(0)
		}
	case "exit-one":
		_ = os.WriteFile(".native-started", []byte("native started"), 0600)
		fmt.Fprint(os.Stderr, "native diagnostic")
		os.Exit(1)
	case "large-output":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 128<<10))
	}
	_ = os.WriteFile(".native-started", []byte("native started"), 0600)
	fmt.Fprintln(os.Stdout, "git version 2.55.0")
	os.Exit(0)
}

func localGitNativeFixture(t *testing.T, mode string) (localGitRequest, string) {
	t.Helper()
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(directory, "git")
	if err := os.Symlink(executable, git); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(directory, ".native-case"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, ".native-started")
	return gitMetadataTestRequest(t, git, directory, "version"), marker
}

func TestLocalGitNativeGateRefusesChangedSourceBeforeGO(t *testing.T) {
	request, marker := localGitNativeFixture(t, "normal")
	failure := errors.New("source changed")
	code, retained, err := runLocalGitOwner(t.Context(), request, nil, io.Discard, io.Discard, localGitTestSource{path: request.Directory, err: failure})
	if !errors.Is(err, failure) || retained != nil || code != -1 {
		t.Fatalf("changed source was not a joined pre-GO refusal: code=%d retained=%v err=%v", code, retained != nil, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native Git started before the source gate")
	}
}

func TestLocalGitNativeCompletionAndExitCode(t *testing.T) {
	for _, mode := range []string{"normal", "exit-one"} {
		t.Run(mode, func(t *testing.T) {
			request, marker := localGitNativeFixture(t, mode)
			var output, diagnostic bytes.Buffer
			code, retained, err := runLocalGitOwner(t.Context(), request, nil, &output, &diagnostic, localGitTestSource{path: request.Directory})
			if retained != nil {
				t.Fatalf("ordinary native operation lost cleanup: %v", err)
			}
			if _, markerErr := os.Stat(marker); markerErr != nil {
				t.Fatal(markerErr)
			}
			if mode == "normal" && (code != 0 || err != nil || output.String() != "git version 2.55.0\n") {
				t.Fatalf("joined success: %d, %q, %v", code, output.String(), err)
			}
			if mode == "exit-one" && (code != 1 || err == nil || diagnostic.String() != "native diagnostic") {
				t.Fatalf("native exit state lost: %d, %q, %v", code, diagnostic.String(), err)
			}
		})
	}
}

func TestLocalGitNativeLeaderExitCannotClaimResidualSuccess(t *testing.T) {
	request, _ := localGitNativeFixture(t, "residual")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	code, retained, err := runLocalGitOwner(ctx, request, nil, io.Discard, io.Discard, localGitTestSource{path: request.Directory})
	if retained != nil || code != 0 || err == nil || !strings.Contains(err.Error(), "descendants outlived") {
		t.Fatalf("direct exit was mistaken for complete native success: code=%d retained=%v err=%v", code, retained != nil, err)
	}
}

func TestLocalGitNativeCancelOwnsForkDuringCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "cancel-fork"} {
		t.Run(mode, func(t *testing.T) {
			request, marker := localGitNativeFixture(t, mode)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() {
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-tick.C:
						if _, err := os.Stat(marker); err == nil {
							cancel()
							return
						}
					case <-deadline.C:
						cancel()
						return
					case <-ctx.Done():
						return
					}
				}
			}()
			_, retained, err := runLocalGitOwner(ctx, request, nil, io.Discard, io.Discard, localGitTestSource{path: request.Directory})
			if retained != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("native cancellation did not join its group: retained=%v err=%v", retained != nil, err)
			}
		})
	}
}

func TestLocalGitNativeParentEOFBeforeGONeverStartsGit(t *testing.T) {
	request, marker := localGitNativeFixture(t, "normal")
	owner, err := startLocalGitOwner(request, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLocalGitRequest(owner.control, request); err != nil {
		t.Fatal(err)
	}
	if err := <-owner.ready; err != nil {
		t.Fatal(err)
	}
	owner.cancel()
	select {
	case <-owner.done:
		if !owner.known {
			t.Fatal("pre-GO parent EOF was not joined")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pre-GO EOF leaked an owner")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native Git started after parent EOF before GO")
	}
}

func TestLocalGitNativeParentEOFAfterGOJoinsNativePack(t *testing.T) {
	request, marker := localGitNativeFixture(t, "cancel")
	owner, err := startLocalGitOwner(request, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.cancel()
	if err := writeLocalGitRequest(owner.control, request); err != nil {
		t.Fatal(err)
	}
	if err := <-owner.ready; err != nil {
		t.Fatal(err)
	}
	if _, err := owner.control.Write([]byte{'G'}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the native pack did not reach its readiness gate")
		}
		time.Sleep(time.Millisecond)
	}
	owner.cancel() // the same pipe EOF produced by parent death, no PID signal
	select {
	case <-owner.done:
		if !owner.known || owner.err == nil {
			t.Fatalf("post-GO EOF did not join cancellation: known=%v err=%v", owner.known, owner.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("post-GO EOF retained native work")
	}
}

func TestLocalGitNativeOutputCapJoinsBeforeReturning(t *testing.T) {
	request, _ := localGitNativeFixture(t, "large-output")
	output := &cappedGitOutput{limit: 4096}
	_, retained, err := runLocalGitOwner(t.Context(), request, nil, output, io.Discard, localGitTestSource{path: request.Directory})
	if retained != nil || err == nil || !output.exceeded || output.Len() > output.limit {
		t.Fatalf("native first-output cap lost the owner: retained=%v length=%d err=%v", retained != nil, output.Len(), err)
	}
}

func TestLocalGitNativeCancelledStartHasNoNativeMarker(t *testing.T) {
	request, marker := localGitNativeFixture(t, "normal")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, retained, err := runLocalGitOwner(ctx, request, nil, io.Discard, io.Discard, localGitTestSource{path: request.Directory})
	if retained != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start changed ownership: retained=%v err=%v", retained != nil, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cancelled start launched native Git")
	}
}

func TestLocalGitNativeRejectsUnfixedExecutable(t *testing.T) {
	request, marker := localGitNativeFixture(t, "normal")
	request.Executable = "/not-the-fixed-git"
	_, retained, err := runLocalGitOwner(t.Context(), request, nil, io.Discard, io.Discard, localGitTestSource{path: request.Directory})
	if retained != nil || err == nil {
		t.Fatalf("unfixed executable changed ownership: retained=%v err=%v", retained != nil, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unfixed executable launched native work")
	}
}
