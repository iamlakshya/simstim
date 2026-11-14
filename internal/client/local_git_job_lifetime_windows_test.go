//go:build windows

package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const localGitParentDeathFixture = "local-git-parent-death-fixture-v1"

func init() {
	if len(os.Args) == 5 && os.Args[1] == localGitParentDeathFixture {
		os.Exit(runLocalGitParentDeathFixture())
	}
	mode, err := readGitFixtureCase()
	if err != nil || mode != "parent-death" {
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "local-git-parent-death-pack-v1" {
		publishLocalGitFixtureProcess("pack")
		time.Sleep(time.Minute) // finite if a deliberately broken mutant loses the Job
		os.Exit(0)
	}
	if strings.EqualFold(filepath.Base(os.Args[0]), "git.exe") && os.Args[len(os.Args)-1] == "version" {
		publishLocalGitFixtureProcess("git")
		executable, _ := os.Executable()
		child := exec.Command(executable, "local-git-parent-death-pack-v1")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(124)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

// This closed test fixture intentionally receives the still-live observer's
// native PROCESS_DUP_HANDLE handle as stdout. It duplicates ITS OWN process
// object into the observer and reports that real handle through stderr. The
// observer never reconstructs or operates on a numeric process ID.
func publishLocalGitFixtureProcess(kind string) {
	var remote windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentProcess(), windows.Handle(os.Stdout.Fd()),
		&remote, windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0); err != nil {
		os.Exit(123)
	}
	fmt.Fprintf(os.Stderr, "%s %d\n", kind, remote)
}

type localGitFixtureT interface {
	Helper()
	Fatal(...any)
}

func startLocalGitFixtureGuardian(t localGitFixtureT, assignment windows.Handle, output, diagnostic *os.File, request localGitRequest) (*os.Process, *os.File, *os.File) {
	t.Helper()
	pipes, err := newLocalGitWindowsPipes()
	if err != nil {
		t.Fatal(err)
	}
	control, err := localGitInheritedHandle(windows.Handle(pipes.controlRead.Fd()), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(control)
	status, err := localGitInheritedHandle(windows.Handle(pipes.statusWrite.Fd()), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(status)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{executable, localGitWindowsMode, fmt.Sprint(control), fmt.Sprint(status), fmt.Sprint(assignment)}
	process, err := os.StartProcess(executable, args, &os.ProcAttr{Env: slices.Clone(request.HelperEnv), Dir: request.HelperDirectory,
		Files: []*os.File{pipes.inputRead, output, diagnostic},
		Sys:   &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(control), syscall.Handle(status), syscall.Handle(assignment)}}})
	if err != nil {
		pipes.close()
		t.Fatal(err)
	}
	for _, file := range []*os.File{pipes.controlRead, pipes.statusWrite, pipes.inputRead, pipes.inputWrite,
		pipes.outputRead, pipes.outputWrite, pipes.errorRead, pipes.errorWrite} {
		file.Close()
	}
	return process, pipes.controlWrite, pipes.statusRead
}

func TestLocalGitWindowsAssignmentFailureNeverStartsNativeGit(t *testing.T) {
	request, marker := localGitWindowsFixture(t, "normal")
	job, err := newLocalGitJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.close()
	// A real valid Job handle with QUERY only: native Assign must fail with
	// access denied. This is neither a malformed-handle nor fake-API witness.
	queryOnly, err := localGitInheritedHandle(job.handle, 0x0004, false)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(queryOnly)
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	process, control, status := startLocalGitFixtureGuardian(t, queryOnly, null, null, request)
	defer control.Close()
	owner := &localGitOwner{ready: make(chan error, 1)}
	result := make(chan localGitWindowsResult, 1)
	go func() { result <- owner.readWindowsStatus(status) }()
	_ = writeLocalGitRequest(control, request)
	state, err := process.Wait()
	if err != nil || state.Success() {
		t.Fatalf("native assignment failure was not joined: state=%v err=%v", state, err)
	}
	if got := <-result; got.err == nil || !strings.Contains(got.err.Error(), "assignment failed") {
		t.Fatalf("real assignment failure was hidden: %+v", got)
	}
	if active, err := job.active(); err != nil || active != 0 {
		t.Fatalf("failed assignment invented membership: active=%d err=%v", active, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native Git started after a failed Job assignment")
	}
}

// fixtureTB is used only by the closed self-process before the Go test runner.
// Its failures terminate that same trusted fixture, closing the actual Job.
type fixtureTB struct{}

func (fixtureTB) Helper()           {}
func (fixtureTB) Fatal(args ...any) { fmt.Fprintln(os.Stderr, args...); os.Exit(122) }

func runLocalGitParentDeathFixture() int {
	control, err := localGitPipeHandle(os.Args[2])
	if err != nil {
		return 120
	}
	events, err := localGitPipeHandle(os.Args[3])
	if err != nil {
		return 120
	}
	observer, err := localGitHandleNumber(os.Args[4])
	if err != nil {
		return 120
	}
	job, err := newLocalGitJob()
	if err != nil {
		return 120
	}
	assignment, err := localGitInheritedHandle(job.handle, 0x0001, false)
	if err != nil {
		return 120
	}
	helperEnv, err := helperGitMetadataEnvironment()
	if err != nil {
		return 120
	}
	frame, err := os.Open(".native-request")
	if err != nil {
		return 120
	}
	request, err := readLocalGitRequest(frame)
	frame.Close()
	if err != nil {
		return 120
	}
	request.HelperEnv = helperEnv
	request.HelperDirectory, _ = os.Getwd()
	observerFile := os.NewFile(uintptr(observer), "live-fixture-observer")
	_, gate, status := startLocalGitFixtureGuardian(fixtureTB{}, assignment, observerFile, events, request)
	windows.CloseHandle(assignment)
	owner := &localGitOwner{ready: make(chan error, 1)}
	go owner.readWindowsStatus(status)
	if err := writeLocalGitRequest(gate, request); err != nil {
		return 120
	}
	if err := <-owner.ready; err != nil {
		return 120
	}
	if _, err := gate.Write([]byte{'G'}); err != nil {
		return 120
	}
	var release [1]byte
	_, _ = control.Read(release[:])
	// os.Exit deliberately skips explicit cleanup. The native process exit
	// closes the SOLE Job handle. No PID or TerminateJobObject call helps it.
	os.Exit(77)
	return 77
}

func TestLocalGitWindowsParentDeathClosesLastJobHandle(t *testing.T) {
	request, _ := localGitWindowsFixture(t, "parent-death")
	frame, err := os.OpenFile(filepath.Join(request.Directory, ".native-request"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLocalGitRequest(frame, request); err != nil {
		frame.Close()
		t.Fatal(err)
	}
	if err := frame.Close(); err != nil {
		t.Fatal(err)
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlRead.Close()
	defer controlWrite.Close()
	eventRead, eventWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer eventRead.Close()
	defer eventWrite.Close()
	ctl, err := localGitInheritedHandle(windows.Handle(controlRead.Fd()), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if ctl != 0 {
			windows.CloseHandle(ctl)
		}
	}()
	events, err := localGitInheritedHandle(windows.Handle(eventWrite.Fd()), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if events != 0 {
			windows.CloseHandle(events)
		}
	}()
	observer, err := localGitInheritedHandle(windows.CurrentProcess(), windows.PROCESS_DUP_HANDLE, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if observer != 0 {
			windows.CloseHandle(observer)
		}
	}()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	parent := exec.CommandContext(ctx, executable, localGitParentDeathFixture, fmt.Sprint(ctl), fmt.Sprint(events), fmt.Sprint(observer))
	parent.Env = slices.Clone(request.HelperEnv)
	parent.Dir = request.HelperDirectory
	parent.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(ctl), syscall.Handle(events), syscall.Handle(observer)}}
	parent.Stdout, parent.Stderr = io.Discard, io.Discard
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(ctl)
	windows.CloseHandle(events)
	windows.CloseHandle(observer)
	ctl, events, observer = 0, 0, 0
	controlRead.Close()
	eventWrite.Close() // only the closed native fixture can keep events live
	waited := false
	defer func() {
		if !waited {
			_ = parent.Process.Kill() // actual retained *os.Process handle
			_ = parent.Wait()
		}
	}()
	reader := bufio.NewScanner(eventRead)
	var members []windows.Handle
	for range 2 {
		if !reader.Scan() {
			t.Fatal("the native Git/pack observer handles were not received")
		}
		fields := strings.Fields(reader.Text())
		if len(fields) != 2 || fields[0] != "git" && fields[0] != "pack" {
			t.Fatalf("bad closed fixture event: %q", reader.Text())
		}
		value, err := strconv.ParseUint(fields[1], 10, strconv.IntSize)
		if err != nil {
			t.Fatal(err)
		}
		handle := windows.Handle(value)
		members = append(members, handle)
		defer windows.CloseHandle(handle)
		if state, err := windows.WaitForSingleObject(handle, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
			t.Fatalf("the native fixture was not live before parent death: state=%d err=%v", state, err)
		}
	}
	if _, err := controlWrite.Write([]byte{'X'}); err != nil {
		t.Fatal(err)
	}
	err = parent.Wait()
	waited = true
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 77 {
		t.Fatalf("the owning parent did not exit at its closed gate: %v", err)
	}
	for _, member := range members {
		if state, err := windows.WaitForSingleObject(member, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatalf("last Job close left native work live: state=%d err=%v", state, err)
		}
	}
}
