//go:build windows

package client

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This is the native pointer-free JOBOBJECT_BASIC_ACCOUNTING_INFORMATION from
// WinNT, not a projection of the extended-limit struct. Both supported Windows
// word sizes have size48 and ActiveProcesses at offset40.
type localGitJobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

var (
	_ [48 - unsafe.Sizeof(localGitJobAccounting{})]byte
	_ [unsafe.Sizeof(localGitJobAccounting{}) - 48]byte
	_ [40 - unsafe.Offsetof(localGitJobAccounting{}.ActiveProcesses)]byte
	_ [unsafe.Offsetof(localGitJobAccounting{}.ActiveProcesses) - 40]byte
)

// Only the parent owns this handle after READY. The mutex prevents a late
// cancellation from operating on a closed and possibly reused handle number.
type localGitJob struct {
	mu      sync.Mutex
	handle  windows.Handle
	closed  bool
	stopErr error
}

func newLocalGitJob() (*localGitJob, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create the local Git job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, errors.Join(fmt.Errorf("set the closed local Git job limits: %w", err), windows.CloseHandle(handle))
	}
	return &localGitJob{handle: handle}, nil
}

func (j *localGitJob) stop() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.closed {
		j.stopErr = errors.Join(j.stopErr, windows.TerminateJobObject(j.handle, 125))
	}
}

func (j *localGitJob) active() (uint32, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return 0, fmt.Errorf("the local Git job was already closed")
	}
	var accounting localGitJobAccounting
	var written uint32
	err := windows.QueryInformationJobObject(j.handle, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), &written)
	return localGitAccountingCount(accounting, written, err)
}

func localGitAccountingCount(accounting localGitJobAccounting, written uint32, err error) (uint32, error) {
	if err != nil {
		return 0, fmt.Errorf("query actual local Git job membership: %w", err)
	}
	if written != uint32(unsafe.Sizeof(accounting)) {
		return 0, fmt.Errorf("the local Git job returned incomplete accounting")
	}
	return accounting.ActiveProcesses, nil
}

func (j *localGitJob) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	return windows.CloseHandle(j.handle)
}

func (j *localGitJob) stopFailure() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stopErr
}

func localGitInheritedHandle(handle windows.Handle, access uint32, sameAccess bool) (windows.Handle, error) {
	var duplicate windows.Handle
	var options uint32
	if sameAccess {
		options = windows.DUPLICATE_SAME_ACCESS
	}
	if err := windows.DuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &duplicate, access, true, options); err != nil {
		return 0, fmt.Errorf("duplicate a private local Git handle: %w", err)
	}
	return duplicate, nil
}

func (j *localGitJob) drain(owner *localGitOwner) (bool, error) {
	residual, reported := false, false
	for {
		active, err := j.active()
		if err == nil && active == 0 {
			return residual, nil
		}
		if err != nil && !reported {
			reported = true
			close(owner.uncertain) // retain the REAL handle; never invent zero
		}
		if !residual {
			residual = true
			j.stop()
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func localGitPipeHandle(text string) (*os.File, error) {
	handle, err := localGitHandleNumber(text)
	if err != nil {
		return nil, err
	}
	kind, err := windows.GetFileType(handle)
	if err != nil || kind != windows.FILE_TYPE_PIPE {
		return nil, fmt.Errorf("the local Git control is not a private pipe")
	}
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, fmt.Errorf("close native control inheritance: %w", err)
	}
	return os.NewFile(uintptr(handle), "local-git-private-control"), nil
}
