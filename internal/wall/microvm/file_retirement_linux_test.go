//go:build linux

package microvm

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func unfilteredRetirementFixtureV2(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	if err := actualUnfilteredCloseThreadV2(); err != nil {
		t.Skip("an admitted unfiltered native close thread is required: " + err.Error())
	}
}

func TestMicroVMFileRetirementKeepsLateDiagnosticWithoutFDReuseV2(t *testing.T) {
	path := os.Getenv("VM_REAL_LATE_CLOSE_FILE")
	if path == "" {
		t.Skip("Root must admit a real native filesystem fixture whose first concrete close returns a late errno")
	}
	unfilteredRetirementFixtureV2(t)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatal("the admitted real close fixture path is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		t.Fatal("the real close fixture is not a regular native file", err)
	}
	originalFD := file.Fd()
	owner := &microVMFileRetirementV2{file: file}
	first := owner.closeV2()
	if first == nil || errors.Is(first, os.ErrClosed) || !owner.retired || !owner.attempted {
		t.Fatal("the first real late close did not retain its diagnostic and retire its actual fd", first)
	}
	foreign, err := os.Create(filepath.Join(t.TempDir(), "new-file"))
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := owner.closeV2(); err != nil {
		t.Fatal("retired retry touched the old numeric slot", err)
	}
	if _, err := foreign.Stat(); err != nil {
		t.Fatal("retired retry closed a new actual file", err)
	}
	if !errors.Is(owner.diagnosticV2(), first) {
		t.Fatal("the first close diagnostic was erased")
	}
	if foreign.Fd() != originalFD {
		t.Log("the native allocator did not reuse the exact slot; the foreign-file preservation check still passed")
	}
}

func TestMicroVMFileRetirementAlreadyClosedStaysUnknownV2(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	file, err := os.Create(filepath.Join(t.TempDir(), "original"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	foreign, err := os.Create(filepath.Join(t.TempDir(), "foreign"))
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	owner := &microVMFileRetirementV2{file: file}
	first := owner.closeV2()
	if first == nil || owner.retired || !errors.Is(first, os.ErrClosed) {
		t.Fatal("an already-closed object became a native retirement proof", first)
	}
	if err := owner.closeV2(); err == nil {
		t.Fatal("an unknown close was normalised into success")
	}
	if _, err := foreign.Stat(); err != nil {
		t.Fatal("unknown retry touched a reused native slot", err)
	}
}

func TestMicroVMFileRetirementJoinsActualBorrowerV2(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	file, err := os.Create(filepath.Join(t.TempDir(), "borrowed"))
	if err != nil {
		t.Fatal(err)
	}
	owner := &microVMFileRetirementV2{file: file}
	borrowed, release, err := owner.borrowV2()
	if err != nil {
		t.Fatal(err)
	}
	started, done := make(chan struct{}), make(chan error, 1)
	go func() { close(started); done <- owner.closeV2() }()
	<-started
	if _, err := borrowed.Stat(); err != nil {
		release()
		t.Fatal("close retired an active original borrower", err)
	}
	select {
	case err := <-done:
		release()
		t.Fatal("close did not join the actual borrower", err)
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !owner.retired {
		t.Fatal("joined close did not retire the original file")
	}
}

func TestMicroVMFileRetirementRejectsFilteredCloseErrnoV2(t *testing.T) {
	if os.Getenv("VM_CLOSE_FILTER_FIXTURE") == "1" {
		filteredCloseFixtureV2(t)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMicroVMFileRetirementRejectsFilteredCloseErrnoV2$", "-test.v")
	command.Env = append(os.Environ(), "VM_CLOSE_FILTER_FIXTURE=1")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native filtered-close fixture: %v\n%s", err, out)
	}
}

func filteredCloseFixtureV2(t *testing.T) {
	runtime.LockOSThread()
	file, err := os.Create(filepath.Join(t.TempDir(), "filtered"))
	if err != nil {
		t.Fatal(err)
	}
	fd := file.Fd()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	// The real kernel filter blocks THIS close before it can retire the fd.
	// No close callback or production-function replacement is injected.
	instructions := []unix.SockFilter{
		{Code: 0x20, K: 0}, {Code: 0x15, K: uint32(unix.SYS_CLOSE), Jf: 3},
		{Code: 0x20, K: 16}, {Code: 0x15, K: uint32(fd), Jf: 1},
		{Code: 0x06, K: 0x00050000 | uint32(unix.EPERM)}, {Code: 0x06, K: 0x7fff0000},
	}
	program := unix.SockFprog{Len: uint16(len(instructions)), Filter: &instructions[0]}
	//nolint:gosec // The isolated native fixture passes its bounded BPF to seccomp.
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(instructions)
	if errno != 0 {
		t.Fatal(errno)
	}
	owner := &microVMFileRetirementV2{file: file}
	first := owner.closeV2()
	var stat unix.Stat_t
	if first == nil || owner.retired || unix.Fstat(int(fd), &stat) != nil {
		t.Fatal("a synthetic close errno became retirement", first)
	}
	if err := owner.closeV2(); err == nil {
		t.Fatal("an unknown filtered close became a successful retry")
	}
	os.Exit(0) // The fixture process ends the still-open fd; no unsafe raw retry.
}
