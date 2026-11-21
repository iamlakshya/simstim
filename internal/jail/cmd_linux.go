package jail

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/slotlease"
	"golang.org/x/sys/unix"
)

// Cmd is a jail's command, ready to start. Its Stdin, Stdout and Stderr are
// files or nil: a jail copies no stdio. Its pipes give the other end of an
// os.Pipe. It keeps the jail's process attributes to itself, so a caller
// cannot take away the jail's new session, or its namespaces.
type Cmd struct {
	modeExecV1         *vmmExecOwnerV1
	publicationLeaseV1 *publicationExecLeaseV1
	slotSourceV1       *slotlease.SourceV1
	slotInitLeaseV1    *os.File
	slotInitLeaseRefV1 *slotInitLeaseRefV1
	Stdin              io.Reader
	Stdout             io.Writer
	Stderr             io.Writer

	gvisorOuter      *gvisorOuterOwnerV1 // fixed C creator plus actual A creation pidfd
	gvisorFreezer    *gvisorFreezerV1    // original creator and all descendants share this child
	gvisorFreezerOff string
	ctx              context.Context
	cmd              *exec.Cmd
	setup            []byte             // the first stage's set-up, sent on a pipe at Start
	files            []*os.File         // Spec.Files, from fd 3
	fileRulesV1      []pinnedFileRuleV1 // identity of the selected final Files slots
	binds            []bind             // the pinned binds, as Command resolved them
	rules            []rule             // the pinned rules, with their original paths and identities
	pins             []*os.File         // the pinned binds and rules, which the first stage inherits
	status           *os.File           // the read end of the pipe on which init says how the command ended
	pipes            [3]*os.File        // the jail's ends of StdinPipe, StdoutPipe and StderrPipe
	ends             []*os.File         // the caller's ends of those pipes

	mu           sync.Mutex
	phase        phase
	pid          int  // published only after exec.Cmd.Start returns
	reaped       bool // the first stage was reaped, so its process group may be gone
	pidfd        int  // created with this child, never recovered from pid
	owned        bool
	physical     bool // actual POLLIN on this creation pidfd, or no child was started
	groupUnsafe  bool // a failed wait revoked numeric group signalling
	lifecycleErr error
	abort        chan struct{} // closed by Signal during the set-up
	abortOnce    sync.Once
	done         chan struct{} // logical completion; never physical-exit evidence
	doneOnce     sync.Once
	exit         *ExitError
}

// phase is how far a Cmd has gone.
type phase int

const (
	phaseNew       phase = iota // Start was not called
	phaseSetup                  // the first stage runs, and reads its set-up
	phaseRunning                // the set-up is in, and Wait was not called
	phaseWaiting                // Wait was called
	phaseFailed                 // Start failed, and reaped what it started
	phaseUncertain              // logical wait failed; exact native child ownership is retained
)

// Start starts the jail. Its first stage is a new session, and the leader
// of the process group that holds every process of the jail. Start sends
// the set-up while the context, and Signal, can still stop it: then it
// kills and reaps the stage, and returns an error.
func (c *Cmd) Start() error {
	c.mu.Lock()
	if c.phase != phaseNew {
		c.mu.Unlock()
		return failure("the jail was already started", "Make a new jail with jail.Command.")
	}
	c.phase = phaseSetup
	c.mu.Unlock()
	// The private outer source may lock this exact creation thread. Release
	// it on every real Start path; it cannot migrate to another goroutine.
	defer c.releaseVMMCreationThreadV1()
	defer c.closePins()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		// No child, or a positively joined original init, cannot create a
		// future writer. An uncertain Start keeps this SAME original OFD.
		if c.physical {
			c.lifecycleErr = errors.Join(c.lifecycleErr, c.closePublicationCaptureV1())
		}
	}()
	if c.slotInitLeaseV1 != nil {
		defer c.slotInitLeaseV1.Close()
	}
	if c.gvisorOuter != nil {
		defer func() {
			// An uncertain native lifetime retains the SAME code objects as its
			// namespace owner. Successful starts close them only at physical join.
			if c.PhysicalExitV1() {
				_ = c.gvisorOuter.closeCodeV1()
			}
		}()
	}
	// Before the jail starts, a failure closes both ends of each pipe.
	early := func(err error) error {
		if settleErr := c.vmmNoCreationV1(); settleErr != nil {
			return c.uncertainWait(errors.Join(err, settleErr))
		}
		if settleErr := c.settleVMMNoFutureBeforeWaitV1(); settleErr != nil {
			return c.uncertainWait(errors.Join(err, settleErr))
		}
		c.closeEnds(true)
		c.mu.Lock()
		c.phase = phaseFailed
		c.physical = true
		c.lifecycleErr = errors.Join(c.lifecycleErr, err)
		c.mu.Unlock()
		return err
	}
	if err := c.ctx.Err(); err != nil {
		return early(err)
	}
	files, release, err := c.stdio()
	if err != nil {
		return early(err)
	}
	defer release()
	if err := checkCapturedFileRulesV1(c.fileRulesV1, files); err != nil {
		return early(err)
	}
	if err := c.samePaths(); err != nil {
		return early(err)
	}
	if c.gvisorOuter != nil {
		return c.startGvisorOuterV1(files, release)
	}
	modeFiles, err := c.vmmCapturedFilesV1()
	if err != nil {
		return early(err)
	}
	if len(modeFiles) != 0 && c.slotInitLeaseV1 != nil {
		return early(failure("the VMM mode source cannot mix with an unjoined slot purpose", "Keep the original runtime and use a complete native slot and mode cohort."))
	}
	if c.slotSourceV1 != nil {
		if err := c.slotSourceV1.CheckCurrentV1(c.ctx); err != nil {
			return early(err)
		}
		if err := checkSlotInitLeaseFileV1(c.slotInitLeaseV1, c.slotInitLeaseRefV1); err != nil {
			return early(err)
		}
	}
	publicationFiles, err := c.publicationCapturedFilesV1()
	if err != nil {
		return early(err)
	}
	setupR, setupW, err := os.Pipe()
	if err != nil {
		return early(failure("cannot make the jail's pipe: "+err.Error(), "Check the open-file limit."))
	}
	statusR, statusW, err := os.Pipe()
	if err != nil {
		_, _ = setupR.Close(), setupW.Close()
		return early(failure("cannot make the jail's pipe: "+err.Error(), "Check the open-file limit."))
	}
	c.cmd.ExtraFiles = append(append(slices.Clone(files), setupR, statusW), c.pins...)
	c.cmd.ExtraFiles = append(c.cmd.ExtraFiles, publicationFiles...)
	if c.slotInitLeaseV1 != nil {
		c.cmd.ExtraFiles = append(c.cmd.ExtraFiles, c.slotInitLeaseV1)
	}
	c.cmd.ExtraFiles = append(c.cmd.ExtraFiles, modeFiles...)
	createdFD := -1
	attr := syscall.SysProcAttr{}
	if c.cmd.SysProcAttr != nil {
		attr = *c.cmd.SysProcAttr
	}
	attr.PidFD = &createdFD
	c.cmd.SysProcAttr = &attr
	err = c.cmd.Start()
	release() // the child has its own copies, or no child was created
	if err == nil {
		c.mu.Lock()
		c.pid = c.cmd.Process.Pid
		c.pidfd, c.owned = createdFD, true
		c.mu.Unlock()
	}
	_, _ = setupR.Close(), statusW.Close()
	c.closeEnds(false) // the jail has its own copies now
	if err != nil {
		_, _ = setupW.Close(), statusR.Close()
		return early(failure("cannot start the jail: "+err.Error(), "Check that this program can start itself again."))
	}
	c.status = statusR
	if createdFD < 0 {
		c.mu.Lock()
		c.groupUnsafe = true
		c.phase = phaseUncertain
		c.lifecycleErr = errors.New("the kernel returned no creation pidfd for the owned jail child")
		cause := c.lifecycleErr
		c.mu.Unlock()
		setupW.Close()
		c.closeEnds(true)
		return stepFailed("cannot retain native jail ownership", "Use a supported kernel with creation pidfds.", cause)
	}
	unix.CloseOnExec(createdFD)
	if nativeErr := c.vmmCreatedRootV1(createdFD); nativeErr != nil {
		_ = setupW.Close()
		c.mu.Lock()
		c.lifecycleErr = errors.Join(c.lifecycleErr, nativeErr)
		c.mu.Unlock()
		if killErr := c.killGroup(); killErr != nil {
			return c.uncertainWait(errors.Join(nativeErr, killErr))
		}
		if waitErr := waitOwnedPIDFD(createdFD, -1); waitErr != nil {
			return c.uncertainWait(errors.Join(nativeErr, waitErr))
		}
		result := c.completeOwnedWait(false)
		c.mu.Lock()
		if c.physical {
			c.phase = phaseFailed
		}
		c.mu.Unlock()
		return errors.Join(nativeErr, result)
	}
	written := make(chan error, 1)
	go func() {
		n, err := setupW.Write(c.setup)
		if err == nil && n != len(c.setup) {
			err = io.ErrShortWrite
		}
		written <- errors.Join(err, setupW.Close())
	}()
	writePending := true
	select {
	case err = <-written:
		writePending = false
		if err != nil {
			err = stepFailed("the jail ended during its set-up", "Read the jail's stderr.", err)
		}
	case <-c.ctx.Done():
		err = c.ctx.Err()
	case <-c.abort:
		err = errSignalled
	}
	if err == nil {
		if afterSetup != nil {
			afterSetup(c)
		}
		err = c.toRunning()
	}
	if err != nil {
		closeErr := setupW.Close()
		var writeErr error
		if writePending {
			writeErr = <-written
		}
		killErr := c.killGroup()
		c.mu.Lock()
		c.lifecycleErr = errors.Join(c.lifecycleErr, err, closeErr, writeErr, killErr)
		c.mu.Unlock()
		if killErr != nil {
			return c.uncertainWait(killErr)
		}
		if waitErr := waitOwnedPIDFD(createdFD, -1); waitErr != nil {
			return c.uncertainWait(waitErr)
		}
		result := c.completeOwnedWait(true)
		c.mu.Lock()
		if c.physical {
			c.phase = phaseFailed
		}
		c.mu.Unlock()
		return errors.Join(err, result)
	}
	go c.watch()
	return nil
}

// errSignalled is the error of a jail that Signal stopped during its set-up.
var errSignalled = failure("the jail was signalled during its set-up, so it was killed", "Signal the jail after Start returns.")

// afterSetup, when a test sets it, runs between the set-up's write and the
// move to running.
var afterSetup func(*Cmd)

// toRunning moves the jail from its set-up to running, under the mutex, so
// that a Signal or a cancel that came after the set-up's write still stops
// the jail.
func (c *Cmd) toRunning() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.abort:
		return errSignalled
	default:
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	c.phase = phaseRunning
	return nil
}

func (c *Cmd) setPhase(p phase) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.phase = p
}

func (c *Cmd) closePins() {
	for _, f := range c.pins {
		_ = f.Close()
	}
	c.pins = nil
}

// Pid returns the process ID of the jail's first stage, which leads its
// process group, or 0 before Start.
func (c *Cmd) Pid() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pid
}

// Run starts the jail and waits for it.
func (c *Cmd) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// CombinedOutput runs the jail and returns its stdout and stderr together.
// It reads them from one pipe, until every process of the jail is gone.
func (c *Cmd) CombinedOutput() ([]byte, error) {
	c.mu.Lock()
	if c.phase != phaseNew {
		c.mu.Unlock()
		return nil, failure("the jail was already started", "Call CombinedOutput on a new jail only.")
	}
	if hasStream(c.Stdout) || hasStream(c.Stderr) || c.pipes[1] != nil || c.pipes[2] != nil {
		c.mu.Unlock()
		return nil, failure("the jail's Stdout or Stderr is already set", "Use Run, or leave Stdout and Stderr unset.")
	}
	r, w, err := os.Pipe()
	if err != nil {
		c.mu.Unlock()
		return nil, failure("cannot make the jail's pipe: "+err.Error(), "Check the open-file limit.")
	}
	defer func() { _ = r.Close() }()
	c.Stdout, c.Stderr = w, w
	c.mu.Unlock()
	out := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		out <- b
	}()
	err = c.Start()
	_ = w.Close()
	if err == nil {
		err = c.Wait()
	}
	// On uncertain physical completion the child may retain its stream.
	// Close only our reader, so returning the owned failure cannot hang on EOF.
	if err != nil && !c.PhysicalExitV1() {
		_ = r.Close()
	}
	return <-out, err
}

// ExitCode returns the command's exit code, or -1 before Wait, and when a
// signal ended it.
func (c *Cmd) ExitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exit == nil {
		return -1
	}
	return c.exit.Code
}

// exitStatus is how init writes the command's end to its status pipe.
func exitStatus(ws syscall.WaitStatus) string {
	if ws.Signaled() {
		return "signal " + strconv.Itoa(int(ws.Signal()))
	}
	return "exit " + strconv.Itoa(ws.ExitStatus())
}

func parseStatus(s string) *ExitError {
	kind, n, _ := strings.Cut(strings.TrimSpace(s), " ")
	if n == "" || strings.Trim(n, "0123456789") != "" {
		return nil // no status: the jail ended before its command did
	}
	v, _ := strconv.Atoi(n)
	switch kind {
	case "exit":
		return &ExitError{Code: v}
	case "signal":
		return &ExitError{Code: -1, Signal: syscall.Signal(v)}
	default:
		return nil
	}
}

func exitOf(st *os.ProcessState) *ExitError {
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return &ExitError{Code: -1, Signal: ws.Signal()}
	}
	return &ExitError{Code: st.ExitCode()}
}
