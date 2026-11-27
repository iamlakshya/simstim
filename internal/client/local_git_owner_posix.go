//go:build linux || darwin

package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

func localGitCancelGrace() time.Duration { return 2 * time.Second }

const (
	localGitAnchorMode   = "local-git-anchor-v1"
	localGitGuardianMode = "local-git-guardian-v1"
)

// Self helpers are private fixed modes. They run before any application, test
// runner, settings reader or remote connection. There is no shell dispatch.
func init() {
	if len(os.Args) != 2 {
		return
	}
	switch os.Args[1] {
	case localGitAnchorMode:
		os.Exit(localGitAnchor())
	case localGitGuardianMode:
		os.Exit(localGitGuardian())
	}
}

func localGitControls(nonblockControl bool) (*os.File, *os.File, error) {
	for _, fd := range []int{3, 4} {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			return nil, nil, errors.New("the local Git control is not a private pipe")
		}
		syscall.CloseOnExec(fd)
	}
	if nonblockControl {
		// Wrap FD 3 nonblocking so os.NewFile registers it with Go's netpoller;
		// otherwise control.Close() cannot wake a blocked control.Read() while
		// the parent still holds its write end open. Never call control.Fd().
		if err := syscall.SetNonblock(3, true); err != nil {
			return nil, nil, fmt.Errorf("set nonblocking local Git control: %w", err)
		}
	}
	control, status := os.NewFile(3, "local-git-control"), os.NewFile(4, "local-git-status")
	if control == nil || status == nil {
		return nil, nil, errors.New("the local Git private control is missing")
	}
	return control, status, nil
}

func startLocalGitOwner(request localGitRequest, input io.Reader, output, diagnostic io.Writer) (*localGitOwner, error) {
	if err := validateGitRoleEnvironment(localGitHelper, request.HelperEnv, false); err != nil {
		return nil, err
	}
	if output == nil {
		output = io.Discard
	}
	if diagnostic == nil {
		diagnostic = io.Discard
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the local Git owner executable: %w", err)
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open local Git control: %w", err)
	}
	defer controlRead.Close()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		controlWrite.Close()
		return nil, fmt.Errorf("open local Git status: %w", err)
	}
	defer statusWrite.Close()
	owner := &localGitOwner{control: controlWrite, done: make(chan struct{}), ready: make(chan error, 1),
		uncertain: make(chan struct{}), stdout: &localGitSink{target: output}, stderr: &localGitSink{target: diagnostic}, code: -1}
	command := exec.Command(executable, localGitAnchorMode)
	command.Env = localGitHelperEnv(request.HelperEnv)
	command.Dir = request.HelperDirectory
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdin, command.Stdout, command.Stderr = input, owner.stdout, owner.stderr
	command.ExtraFiles = []*os.File{controlRead, statusWrite}
	owner.command = command
	if err := command.Start(); err != nil {
		controlWrite.Close()
		statusRead.Close()
		return nil, fmt.Errorf("start the local Git session anchor: %w", err)
	}
	go owner.join(statusRead)
	return owner, nil
}

func (o *localGitOwner) join(status *os.File) {
	protocolDone := make(chan struct{})
	go func() {
		defer close(protocolDone)
		defer status.Close()
		reader := bufio.NewScanner(status)
		reader.Buffer(make([]byte, 4096), 64<<10)
		ready := false
		uncertain := false
		for reader.Scan() {
			var update localGitStatus
			if err := json.Unmarshal(reader.Bytes(), &update); err != nil {
				o.err = fmt.Errorf("decode local Git ownership status: %w", err)
				break
			}
			if update.Ready && !ready {
				ready = true
				o.ready <- nil
				continue
			}
			if update.Ready {
				o.err = fmt.Errorf("the local Git owner repeated READY")
				break
			}
			o.code, o.known = update.Code, update.Quiescent
			if update.Failure != "" {
				o.err = fmt.Errorf("local Git owner: %s", update.Failure)
			}
			if !update.Quiescent && !uncertain {
				uncertain = true
				close(o.uncertain)
			}
		}
		if err := reader.Err(); err != nil {
			o.err = errors.Join(o.err, fmt.Errorf("read local Git ownership status: %w", err))
		}
		if !ready {
			o.ready <- fmt.Errorf("the local Git owner did not become ready")
		}
	}()
	waitErr := o.command.Wait() // joins the actual stdin/stdout/stderr copiers
	<-protocolDone
	o.err = errors.Join(o.err, waitErr)
	o.cancel()
	close(o.done)
}

func localGitAnchor() int {
	control, status, err := localGitControls(false)
	if err != nil {
		return 125
	}
	defer control.Close()
	defer status.Close()
	if localGitHelperObservation != nil {
		localGitHelperObservation("anchor")
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	executable, err := os.Executable()
	if err != nil {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the guardian executable is unavailable"})
		return 125
	}
	guardian := exec.Command(executable, localGitGuardianMode)
	environment, err := helperGitMetadataEnvironment()
	if err != nil {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the initial helper metadata role is invalid; Try: use the canonical local source flow"})
		return 125
	}
	guardian.Env = environment
	guardian.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	guardian.Stdin, guardian.Stdout, guardian.Stderr = os.Stdin, os.Stdout, os.Stderr
	guardian.ExtraFiles = []*os.File{control, status}
	if err := guardian.Start(); err != nil {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the guardian could not start"})
		return 125
	}
	// Only G reads the parent pipe. A retains its private session/group until
	// G has finished, including normal parent EOF. A never signals a cached ID.
	control.Close()
	status.Close()
	if err := guardian.Wait(); err != nil {
		return 125
	}
	return 0
}

func localGitGuardian() int {
	control, status, err := localGitControls(true)
	if err != nil {
		return 125
	}
	defer control.Close()
	defer status.Close()
	if _, err := helperGitMetadataEnvironment(); err != nil {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the actual helper startup environment is invalid; Try: use the canonical local source flow"})
		return 125
	}
	if localGitHelperObservation != nil {
		localGitHelperObservation("guardian")
	}
	anchor := os.Getppid()
	if group, err := syscall.Getpgid(0); err != nil || group != os.Getpid() || anchor <= 1 {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the guardian has no private reserved group"})
		return 125
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
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
	return runLocalGitGuardian(control, status, request, signals, anchor)
}

func runLocalGitGuardian(control, status *os.File, request localGitRequest, signals <-chan os.Signal, anchor int) int {
	var gate sync.Mutex
	gateCancelled := false
	cancel := make(chan struct{})
	go func() {
		var extra [1]byte
		_, _ = control.Read(extra[:]) // EOF or any further byte stops the same run
		gate.Lock()
		gateCancelled = true
		gate.Unlock()
		close(cancel)
	}()
	command := exec.Command(request.Executable, localGitArguments(request)...)
	command.Dir, command.Env = request.Directory, request.Env
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	gate.Lock()
	select {
	case <-signals:
		gateCancelled = true
	default:
	}
	var startErr error
	if !gateCancelled {
		startErr = command.Start()
	}
	started := !gateCancelled && startErr == nil
	gate.Unlock()
	if !started {
		sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: -1, Failure: "the native launch did not start"})
		return 0
	}
	direct := make(chan error, 1)
	go func() { direct <- command.Wait() }()
	var directErr error
	directJoined := false
	cancelled := false
	select {
	case directErr = <-direct:
		directJoined = true
	case <-cancel:
		cancelled = true
	case <-signals:
		cancelled = true
	}
	// This process stays alive, reserving its PID as the old group ID. It is
	// not a session leader, so it can join A's still-live group. Only after
	// that succeeds may it query/signal its OWN reserved former group.
	if err := syscall.Setpgid(0, anchor); err != nil {
		sendLocalGitStatus(status, localGitStatus{Code: -1, Failure: "the live guardian could not relocate; captures are retained"})
		for {
			time.Sleep(time.Minute) // retain the actual live owner; never signal after lost ownership
		}
	}
	residual := drainLocalGitGroup(status)
	if !directJoined {
		directErr = <-direct
	}
	control.Close()
	<-cancel // join the sole control reader before reporting completion
	code := 0
	if directErr != nil {
		code = -1
		var exited *exec.ExitError
		if errors.As(directErr, &exited) {
			code = exited.ExitCode()
		}
	}
	failure := ""
	if cancelled {
		failure = "the native operation was cancelled"
	} else if residual {
		failure = "native descendants outlived Git; cleanup completed"
	}
	sendLocalGitStatus(status, localGitStatus{Quiescent: true, Code: code, Failure: failure})
	return 0
}

func drainLocalGitGroup(status *os.File) bool {
	residual, reported := false, false
	started := time.Now()
	for {
		err := syscall.Kill(-os.Getpid(), 0)
		if errors.Is(err, syscall.ESRCH) {
			return residual
		}
		if err != nil {
			if !reported {
				reported = true
				sendLocalGitStatus(status, localGitStatus{Code: -1, Failure: "native group absence is unknown; captures are retained"})
			}
		} else {
			residual = true
			sig := syscall.SIGTERM
			if time.Since(started) >= 250*time.Millisecond {
				sig = syscall.SIGKILL
			}
			// The only negative target is this still-LIVE process's reserved
			// PID, after relocation. No parent ever caches this group identity.
			if err := syscall.Kill(-os.Getpid(), sig); err != nil && !errors.Is(err, syscall.ESRCH) && !reported {
				reported = true
				sendLocalGitStatus(status, localGitStatus{Code: -1, Failure: "native group cleanup is incomplete; captures are retained"})
			}
		}
		if !reported && time.Since(started) >= 2*time.Second {
			reported = true
			sendLocalGitStatus(status, localGitStatus{Code: -1, Failure: "native cleanup is still running; captures are retained"})
		}
		time.Sleep(25 * time.Millisecond)
	}
}
