//go:build windows

package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func init() {
	if len(os.Args) == 2 && os.Args[1] == "local-git-windows-pack-fixture-v1" {
		if _, err := readGitFixtureCase(); err != nil {
			return
		}
		fmt.Fprint(os.Stdout, "native tail\n")
		_ = os.WriteFile(".native-pack-ready", []byte("ready"), 0600)
		time.Sleep(15 * time.Second)
		os.Exit(0)
	}
	if !strings.EqualFold(filepath.Base(os.Args[0]), "git.exe") || os.Args[len(os.Args)-1] != "version" {
		return
	}
	mode, err := readGitFixtureCase()
	if err != nil || mode == "parent-death" {
		return
	}
	if err := validateGitRoleEnvironment(localGitNormal, os.Environ(), true); err != nil {
		os.Exit(121)
	}
	fmt.Fprint(os.Stdout, "native first\n")
	if mode == "residual" || mode == "cancel" {
		executable, _ := os.Executable()
		child := exec.Command(executable, "local-git-windows-pack-fixture-v1")
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
	}
	_ = os.WriteFile(".native-started", []byte("native ready"), 0600)
	if mode == "cancel" {
		time.Sleep(15 * time.Second)
	}
	if mode == "exit-one" {
		fmt.Fprint(os.Stderr, "native diagnostic")
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLocalGitWindowsAccountingRejectsShortAndFailedZero(t *testing.T) {
	if unsafe.Sizeof(localGitJobAccounting{}) != 48 || unsafe.Offsetof(localGitJobAccounting{}.ActiveProcesses) != 40 {
		t.Fatal("native accounting ABI differs from the WinNT contract")
	}
	for _, written := range []uint32{0, 39, 40, 44, 47, 49} {
		if _, err := localGitAccountingCount(localGitJobAccounting{}, written, nil); err == nil {
			t.Fatalf("short or inconsistent accounting %d became zero", written)
		}
	}
	if _, err := localGitAccountingCount(localGitJobAccounting{}, 48, windows.ERROR_ACCESS_DENIED); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("failed query became an empty Job")
	}
	if active, err := localGitAccountingCount(localGitJobAccounting{ActiveProcesses: 7}, 48, nil); err != nil || active != 7 {
		t.Fatalf("actual membership changed: %d %v", active, err)
	}
	for _, text := range []string{"", "0", "-1", "0x12", fmt.Sprint(^uintptr(0)), "18446744073709551616"} {
		if _, err := localGitHandleNumber(text); err == nil {
			t.Fatalf("invalid native handle number accepted: %q", text)
		}
	}
}

func localGitWindowsFixture(t *testing.T, mode string) (localGitRequest, string) {
	t.Helper()
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	git := filepath.Join(directory, "git.exe")
	output, err := os.OpenFile(git, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	if err := errors.Join(copyErr, output.Close()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(directory, ".native-case"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, ".native-started")
	return gitMetadataTestRequest(t, git, directory, "version"), marker
}

func TestLocalGitWindowsNativeJobCompletionPreservesOutputTail(t *testing.T) {
	for _, mode := range []string{"normal", "residual", "exit-one"} {
		t.Run(mode, func(t *testing.T) {
			request, _ := localGitWindowsFixture(t, mode)
			var output, diagnostic bytes.Buffer
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			code, retained, err := runLocalGitOwner(ctx, request, nil, &output, &diagnostic, localGitTestSource{path: request.Directory})
			if retained != nil {
				joinLocalGitWindowsTestOwner(t, retained)
				t.Fatalf("Job cleanup retained on ordinary closed fixture: %v", err)
			}
			switch mode {
			case "normal":
				if err != nil || code != 0 || output.String() != "native first\n" {
					t.Fatalf("native success changed: code=%d output=%q err=%v", code, output.String(), err)
				}
			case "residual":
				if code != 0 || err == nil || !strings.Contains(err.Error(), "descendants outlived") || output.String() != "native first\nnative tail\n" {
					t.Fatalf("residual child or its output tail was lost: code=%d output=%q err=%v", code, output.String(), err)
				}
			case "exit-one":
				if code != 1 || err == nil || diagnostic.String() != "native diagnostic" {
					t.Fatalf("native exit state lost: code=%d diagnostic=%q err=%v", code, diagnostic.String(), err)
				}
			}
		})
	}
}

func TestLocalGitWindowsNativeCancelRetainsUntilActualJobJoin(t *testing.T) {
	request, marker := localGitWindowsFixture(t, "cancel")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err == nil {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	_, retained, err := runLocalGitOwner(ctx, request, nil, io.Discard, io.Discard, localGitTestSource{path: request.Directory})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("original cancellation was lost: %v", err)
	}
	if retained != nil {
		joinLocalGitWindowsTestOwner(t, retained)
	}
}

func TestLocalGitWindowsSourceFailureHasNoNativeMarker(t *testing.T) {
	request, marker := localGitWindowsFixture(t, "normal")
	failure := errors.New("source changed before GO")
	_, retained, err := runLocalGitOwner(t.Context(), request, nil, io.Discard, io.Discard, localGitTestSource{path: request.Directory, err: failure})
	if retained != nil {
		joinLocalGitWindowsTestOwner(t, retained)
	}
	if !errors.Is(err, failure) {
		t.Fatalf("source gate failure lost: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Windows native Git started before GO")
	}
}

func joinLocalGitWindowsTestOwner(t *testing.T, owner *localGitOwner) {
	t.Helper()
	owner.cancel()
	select {
	case <-owner.done:
		if !owner.known {
			t.Fatal("closed Windows fixture did not prove Job absence and all joins")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("closed Windows fixture cleanup did not finish")
	}
}
