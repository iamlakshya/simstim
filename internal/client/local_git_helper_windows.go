//go:build windows

package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"golang.org/x/sys/windows"
)

const localGitWindowsMode = "local-git-job-owner-v1"

func init() {
	if len(os.Args) == 5 && os.Args[1] == localGitWindowsMode {
		os.Exit(localGitWindowsHelper())
	}
}

func localGitHandleNumber(text string) (windows.Handle, error) {
	value, err := strconv.ParseUint(text, 10, strconv.IntSize)
	if err != nil || value == 0 || uintptr(value) == ^uintptr(0) {
		return 0, fmt.Errorf("the private local Git handle is invalid")
	}
	return windows.Handle(value), nil
}

func localGitWindowsHelper() int {
	control, err := localGitPipeHandle(os.Args[2])
	if err != nil {
		return 125
	}
	defer control.Close()
	status, err := localGitPipeHandle(os.Args[3])
	if err != nil {
		return 125
	}
	defer status.Close()
	job, err := localGitHandleNumber(os.Args[4])
	if err != nil {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the private assignment handle is invalid"})
		return 125
	}
	// This is the trusted helper's OWN native pseudo-handle. No OpenProcess,
	// post-spawn Git assignment or breakaway retry exists on this path.
	assignErr := windows.AssignProcessToJobObject(job, windows.CurrentProcess())
	closeErr := windows.CloseHandle(job)
	if assignErr != nil || closeErr != nil {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the closed job assignment failed before native launch"})
		return 125
	}
	if _, err := helperGitMetadataEnvironment(); err != nil {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the actual helper startup environment is invalid; Try: use the canonical local source flow"})
		return 125
	}
	if localGitHelperObservation != nil {
		localGitHelperObservation("job-owner")
	}
	// Only the parent holds the Job from here, including during READY/GO.
	request, err := readLocalGitRequest(control)
	if err != nil || !filepath.IsAbs(request.Directory) {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the closed launch request is invalid"})
		return 125
	}
	executable, err := localGitExecutable(request.Executable, request.ExecutableID)
	if err != nil {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the selected native Git executable changed; Try: prepare the operation again"})
		return 125
	}
	defer executable.Close()
	sendLocalGitStatus(status, localGitStatus{Ready: true})
	var goByte [1]byte
	if _, err := io.ReadFull(control, goByte[:]); err != nil || goByte[0] != 'G' {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the launch gate was cancelled"})
		return 0
	}
	return runLocalGitWindowsHelper(control, status, request)
}

func runLocalGitWindowsHelper(control, status *os.File, request localGitRequest) int {
	var gate sync.Mutex
	gateCancelled := false
	cancel := make(chan struct{})
	go func() {
		var extra [1]byte
		_, _ = control.Read(extra[:])
		gate.Lock()
		gateCancelled = true
		gate.Unlock()
		close(cancel)
	}()
	arguments := append([]string{request.Executable}, localGitArguments(request)...)
	// Inherit the already-assigned Job by CreateProcess's ordinary rules.
	// Only stdio is inherited; private controls were made non-inheritable and
	// the assignment handle was closed before READY. No BREAKAWAY flag.
	gate.Lock()
	var err error
	var process *os.Process
	if !gateCancelled {
		// os/exec also consults PATHEXT for an absolute .exe, and may try
		// git.exe.com if the pinned path disappears. Start the exact selected
		// native filename directly; no extension or current-directory lookup.
		process, err = os.StartProcess(request.Executable, arguments, &os.ProcAttr{Dir: request.Directory, Env: slices.Clone(request.Env), Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
	}
	started := !gateCancelled && err == nil
	gate.Unlock()
	if !started {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the native launch did not start"})
		return 0
	}
	type result struct {
		state *os.ProcessState
		err   error
	}
	direct := make(chan result, 1)
	go func() { state, err := process.Wait(); direct <- result{state, err} }()
	var joined result
	select {
	case joined = <-direct:
	case <-cancel:
		// The parent terminates and queries its same actual Job. Parent death
		// closes its last handle. The helper never guesses a Git process ID.
		return 125
	}
	control.Close()
	<-cancel
	if joined.err != nil || joined.state == nil {
		sendLocalGitStatus(status, localGitStatus{NativeDone: true, Code: -1, Failure: "the direct native Git wait did not complete"})
		return 125
	}
	sendLocalGitStatus(status, localGitStatus{NativeDone: true, Code: joined.state.ExitCode()})
	return 0
}
