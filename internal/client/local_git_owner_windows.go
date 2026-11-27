//go:build windows

package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func localGitCancelGrace() time.Duration { return 0 }

type localGitWindowsPipes struct {
	controlRead, controlWrite *os.File
	statusRead, statusWrite   *os.File
	inputRead, inputWrite     *os.File
	outputRead, outputWrite   *os.File
	errorRead, errorWrite     *os.File
}

func newLocalGitWindowsPipes() (_ *localGitWindowsPipes, err error) {
	p := &localGitWindowsPipes{}
	defer func() {
		if err != nil {
			p.close()
		}
	}()
	for _, pair := range [][2]**os.File{{&p.controlRead, &p.controlWrite}, {&p.statusRead, &p.statusWrite},
		{&p.inputRead, &p.inputWrite}, {&p.outputRead, &p.outputWrite}, {&p.errorRead, &p.errorWrite}} {
		*pair[0], *pair[1], err = os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("open private local Git pipes: %w", err)
		}
	}
	return p, nil
}

func (p *localGitWindowsPipes) close() {
	for _, file := range []*os.File{p.controlRead, p.controlWrite, p.statusRead, p.statusWrite,
		p.inputRead, p.inputWrite, p.outputRead, p.outputWrite, p.errorRead, p.errorWrite} {
		if file != nil {
			_ = file.Close()
		}
	}
}

func startLocalGitOwner(request localGitRequest, input io.Reader, output, diagnostic io.Writer) (_ *localGitOwner, err error) {
	if err := validateGitRoleEnvironment(localGitHelper, request.HelperEnv, true); err != nil {
		return nil, err
	}
	job, err := newLocalGitJob()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, job.close())
		}
	}()
	pipes, err := newLocalGitWindowsPipes()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			pipes.close()
		}
	}()
	var handles []windows.Handle
	defer func() {
		for _, handle := range handles {
			_ = windows.CloseHandle(handle)
		}
	}()
	for _, file := range []*os.File{pipes.controlRead, pipes.statusWrite} {
		duplicate, err := localGitInheritedHandle(windows.Handle(file.Fd()), 0, true)
		if err != nil {
			return nil, err
		}
		handles = append(handles, duplicate)
	}
	// JOB_OBJECT_ASSIGN_PROCESS=0x0001 from WinNT. G receives only the
	// assignment right, closes this duplicate before READY, and passes none on.
	assignment, err := localGitInheritedHandle(job.handle, 0x0001, false)
	if err != nil {
		return nil, err
	}
	handles = append(handles, assignment)
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the local Git owner executable: %w", err)
	}
	args := []string{executable, localGitWindowsMode}
	var inherited []syscall.Handle
	for _, handle := range handles {
		args = append(args, strconv.FormatUint(uint64(handle), 10))
		inherited = append(inherited, syscall.Handle(handle))
	}
	process, err := os.StartProcess(executable, args, &os.ProcAttr{Env: slices.Clone(request.HelperEnv), Dir: request.HelperDirectory,
		Files: []*os.File{pipes.inputRead, pipes.outputWrite, pipes.errorWrite},
		Sys:   &syscall.SysProcAttr{AdditionalInheritedHandles: inherited}})
	if err != nil {
		return nil, fmt.Errorf("start the closed local Git job helper: %w", err)
	}
	// os.StartProcess retains the real native Process handle. No OpenProcess
	// or numeric PID reconstruction is used before assignment or while joining.
	pipes.controlRead.Close()
	pipes.statusWrite.Close()
	pipes.inputRead.Close()
	pipes.outputWrite.Close()
	pipes.errorWrite.Close()
	if input == nil {
		input = bytes.NewReader(nil)
	}
	if output == nil {
		output = io.Discard
	}
	if diagnostic == nil {
		diagnostic = io.Discard
	}
	owner := &localGitOwner{control: pipes.controlWrite, done: make(chan struct{}), ready: make(chan error, 1),
		uncertain: make(chan struct{}), stdout: &localGitSink{target: output}, stderr: &localGitSink{target: diagnostic}, code: -1, nativeStop: job.stop}
	go owner.joinWindows(process, job, pipes, input)
	return owner, nil
}

type localGitWindowsResult struct {
	code int
	err  error
}

func (o *localGitOwner) readWindowsStatus(status *os.File) localGitWindowsResult {
	defer status.Close()
	reader := bufio.NewScanner(status)
	reader.Buffer(make([]byte, 4096), 64<<10)
	ready, final := false, false
	result := localGitWindowsResult{code: -1}
	for reader.Scan() {
		var update localGitStatus
		if err := json.Unmarshal(reader.Bytes(), &update); err != nil {
			result.err = fmt.Errorf("decode local Git job status: %w", err)
			break
		}
		if update.Ready && !ready && !final {
			ready = true
			o.ready <- nil
			continue
		}
		if update.Ready || final {
			result.err = fmt.Errorf("the local Git job helper repeated its status")
			break
		}
		if !update.NativeDone && !update.Quiescent {
			result.err = fmt.Errorf("the local Git job helper omitted its native result")
			break
		}
		final = true
		result.code = update.Code
		if update.Failure != "" {
			result.err = fmt.Errorf("local Git job helper: %s", update.Failure)
		}
	}
	result.err = errors.Join(result.err, reader.Err())
	if !ready {
		o.ready <- fmt.Errorf("the local Git job helper did not become ready")
	}
	if !final {
		result.err = errors.Join(result.err, fmt.Errorf("the local Git job helper did not report a native result"))
	}
	return result
}

func (o *localGitOwner) joinWindows(process *os.Process, job *localGitJob, pipes *localGitWindowsPipes, input io.Reader) {
	protocol := make(chan localGitWindowsResult, 1)
	go func() { protocol <- o.readWindowsStatus(pipes.statusRead) }()
	streams := make(chan error, 3)
	go func() {
		_, err := io.Copy(pipes.inputWrite, input)
		streams <- errors.Join(err, pipes.inputWrite.Close())
	}()
	for _, stream := range []struct {
		read *os.File
		out  io.Writer
	}{{pipes.outputRead, o.stdout}, {pipes.errorRead, o.stderr}} {
		go func() {
			_, err := io.Copy(stream.out, stream.read)
			streams <- errors.Join(err, stream.read.Close())
		}()
	}
	state, waitErr := process.Wait()
	if waitErr != nil || state == nil {
		// No trustworthy helper join: retain the actual Job and captures even
		// if a separate query later happens to show zero. No cached PID action.
		close(o.uncertain)
		job.stop()
		result := <-protocol
		o.err, o.code = errors.Join(waitErr, result.err), result.code
		for range 3 {
			o.err = errors.Join(o.err, <-streams)
		}
		o.cancel()
		close(o.done)
		return
	}
	residual, drainErr := job.drain(o)
	result := <-protocol
	o.code, o.err = result.code, errors.Join(drainErr, result.err)
	if residual {
		o.err = errors.Join(o.err, fmt.Errorf("native descendants outlived Git; job cleanup completed"))
	}
	for range 3 {
		o.err = errors.Join(o.err, <-streams) // retain every output tail; no WaitDelay
	}
	// Positive active0, real helper Wait and ALL copier/control joins precede
	// last-handle close. The caller's context adds no new grace period.
	o.finishControl()
	o.err = errors.Join(o.err, job.stopFailure())
	closeErr := job.close()
	o.err = errors.Join(o.err, closeErr)
	o.known = closeErr == nil
	close(o.done)
}
