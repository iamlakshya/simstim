//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/wall"
)

type sandbox struct {
	driver             *Driver
	spec               wall.Spec
	uid                string
	owner              context.Context
	lease              *os.File
	agentsLease        io.Closer
	mu                 sync.Mutex
	destroyMu          sync.Mutex
	reapMu             sync.Mutex
	started, destroyed bool
	destroying         bool
	preparedReleased   bool
	forced             bool
	command            ownedVMProcess
	physicalExit       bool
	cancel             context.CancelFunc
	connection         net.Conn
	control            *runnerControl
	done               chan struct{}
	exit               wall.Exit
	log                byteTail
	logDone            chan struct{}
	helloChecked       bool
	bootFacts          api.CapProbe
}

var _ wall.Driver = (*Driver)(nil)
var _ wall.Sandbox = (*sandbox)(nil)

func newSandbox(driver *Driver, spec wall.Spec, lease *os.File, mode string, owner context.Context) *sandbox {
	return &sandbox{driver: driver, spec: cloneBindingSpec(spec), lease: lease, uid: mode, owner: owner, done: make(chan struct{}), exit: wall.Exit{Code: -1}}
}

func (s *sandbox) Start(ctx context.Context) (net.Conn, error) {
	s.mu.Lock()
	if s.started || s.destroyed || s.destroying || s.preparedReleased {
		s.mu.Unlock()
		return nil, fmt.Errorf("this microvm cannot be started twice")
	}
	s.started = true
	s.mu.Unlock()
	budget, err := BudgetFor(s.spec.VM.GuestRAM)
	if err != nil {
		return nil, s.startFailed(err)
	}
	if err := s.driver.options.Boundary(ctx, budget); err != nil {
		return nil, s.startFailed(err)
	}
	facts, err := s.driver.options.Facts(ctx)
	if err != nil {
		return nil, s.startFailed(err)
	}
	if !facts.UserNS || !facts.KVM.Usable || !facts.Seccomp || facts.LandlockABI < 1 || !api.SupportsMicroVMKernel(s.driver.Name(), facts.Kernel) {
		return nil, s.startFailed(fmt.Errorf("the microvm needs a supported host kernel, namespace jail, seccomp, Landlock and usable KVM access"))
	}
	if err := noDumpPrerequisite(); err != nil {
		return nil, s.startFailed(err)
	}
	if !jail.Userns() {
		return nil, s.startFailed(fmt.Errorf("the VMM requires the canonical namespace jail; its empty network namespace is unavailable"))
	}
	if s.uid != uidMode(facts) {
		return nil, s.startFailed(fmt.Errorf("the microvm persistent disk identity mode changed before start"))
	}
	subuid := s.uid == "subuid"
	root, err := cachefs.OpenPrivateRoot(s.spec.SessionDir, false)
	if err != nil {
		return nil, s.startFailed(err)
	}
	data, inspectErr := root.Lstat("vm")
	if os.IsNotExist(inspectErr) {
		inspectErr = cachefs.MakePrivateDir(root, "vm")
	} else if inspectErr == nil && (!data.IsDir() || data.Mode()&os.ModeSymlink != 0) {
		inspectErr = fmt.Errorf("VM data path is not an owned directory")
	}
	root.Close()
	if inspectErr != nil {
		return nil, s.startFailed(inspectErr)
	}
	helper, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, s.startFailed(err)
	}
	_, err = s.driver.prepareDisk(ctx, vmDataDir(s.spec), s.spec.DiskMax, subuid, helper)
	helper.Close()
	if err != nil {
		return nil, s.startFailed(err)
	}
	files, err := s.driver.openBootFiles(ctx, s.spec)
	if err != nil {
		return nil, s.startFailed(err)
	}
	defer files.close()
	if err := s.driver.options.Reference(ctx, s.spec); err != nil {
		return nil, s.startFailed(err)
	}
	s.mu.Lock()
	s.agentsLease, files.agentsLease = files.agentsLease, nil
	s.mu.Unlock()
	// Prepare/Open carry the trusted supervisor's lifetime context. Start's
	// narrower boot context must not kill a healthy VM when startup returns.
	execution, cancel := context.WithCancel(s.owner)
	interrupted := make(chan struct{})
	stopStartup := context.AfterFunc(ctx, func() { cancel(); close(interrupted) })
	startupStopped := false
	stopStartupCancellation := func() {
		if startupStopped {
			return
		}
		startupStopped = true
		if !stopStartup() {
			<-interrupted
		}
	}
	defer stopStartupCancellation()
	s.mu.Lock()
	s.cancel = cancel
	stopped := s.forced
	s.mu.Unlock()
	if stopped {
		cancel()
		return nil, s.startFailed(fmt.Errorf("the microvm was stopped before its VMM could start"))
	}
	boot, cancelBoot := context.WithTimeout(ctx, BootTimeout)
	defer cancelBoot()
	until, _ := boot.Deadline()
	conn, control, command, err := s.launchRunner(execution, files, subuid, until)
	s.mu.Lock()
	s.command, s.control, s.connection = command, control, conn
	s.mu.Unlock()
	if err != nil {
		cancel()
		if command != nil {
			s.mu.Lock()
			s.exit.Err = errors.Join(s.exit.Err, err)
			s.mu.Unlock()
			go s.waitProcess(command, control)
			return nil, err
		}
		return nil, s.startFailed(err)
	}
	go s.waitProcess(command, control)
	if err := control.waitReady(boot); err != nil {
		s.Kill()
		s.Wait()
		return nil, fmt.Errorf("guest connection was not ready: %w", err)
	}
	checked, err := link.ReadGuestHello(boot, conn)
	if err != nil {
		s.Kill()
		s.Wait()
		return nil, fmt.Errorf("guest init did not boot within its deadline: %w", err)
	}
	stopStartupCancellation()
	if ctx.Err() != nil {
		s.Kill()
		s.Wait()
		return nil, ctx.Err()
	}
	if ctx.Err() != nil {
		s.Kill()
		s.Wait()
		return nil, ctx.Err()
	}
	ownedConnection := &ownedGuestLink{Conn: checked}
	s.mu.Lock()
	s.connection = ownedConnection
	s.helloChecked = true
	s.bootFacts = facts
	s.mu.Unlock()
	return ownedConnection, nil
}

func (s *sandbox) startFailed(err error) error {
	err = errors.Join(err, s.releaseAgentsLease(), s.releaseLease())
	if logErr := s.recordDiagnostics(); logErr != nil {
		err = fmt.Errorf("%w; record VMM diagnostics: %v", err, logErr)
	}
	s.mu.Lock()
	s.exit = wall.Exit{Code: -1, Err: err}
	// launchRunner returns every possibly live command even on failure.
	// This path therefore owns no child and can release its preparation.
	s.physicalExit = true
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
	return err
}

func (s *sandbox) recordDiagnostics() error {
	if s.logDone == nil || s.driver.options.Diagnostic == nil {
		return nil
	}
	<-s.logDone
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.driver.options.Diagnostic(ctx, s.spec, s.log.snapshot())
}

// Stop follows the supervisor's guest ctl stop. It waits for the guest to
// sync/unmount/reboot, then kills the owned jail after the supplied grace.
func (s *sandbox) Stop(ctx context.Context, grace time.Duration) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return nil
	}
	if grace < 0 {
		grace = 0
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-s.done:
		return s.Wait().Err
	case <-ctx.Done():
		_ = s.Kill()
		return ctx.Err()
	case <-timer.C:
		err := s.Kill()
		s.Wait()
		return err
	}
}

// PowerButton is the supervisor's bounded fallback when guest ctl stop fails.
func (s *sandbox) PowerButton(ctx context.Context) error {
	s.mu.Lock()
	control := s.control
	s.mu.Unlock()
	if control == nil {
		return fmt.Errorf("the VMM control channel is not running")
	}
	_, err := control.call(ctx, "power-button")
	return err
}

// State reads only the VMM state API; no guest controls or arbitrary URL exists.
func (s *sandbox) State(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	control := s.control
	s.mu.Unlock()
	if control == nil {
		return nil, fmt.Errorf("the VMM control channel is not running")
	}
	return control.call(ctx, "state")
}

func (s *sandbox) Kill() error {
	s.mu.Lock()
	if s.physicalExit {
		s.mu.Unlock()
		return nil
	}
	command, cancel := s.command, s.cancel
	s.forced = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if command != nil {
		err := command.Signal(os.Kill)
		if err != nil {
			s.mu.Lock()
			s.exit.Err = errors.Join(s.exit.Err, err)
			s.mu.Unlock()
		}
		// A logical Wait failure does not revoke this original command.
		// Polling its retained native handle is bounded and never invents a PID.
		s.refreshPhysicalExit()
		return err
	}
	return nil
}
func (s *sandbox) Wait() wall.Exit {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return wall.Exit{Code: -1, Err: fmt.Errorf("the microvm was not started")}
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}
func (s *sandbox) Abilities() wall.Abilities { return wall.Abilities{} }

// CleanExit is owned-process evidence for the canonical state publisher.
// A guest status claim, forced stop or incomplete start cannot establish it.
func (s *sandbox) CleanExit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
	default:
		return false
	}
	return s.cleanExitLocked()
}

// cleanExitLocked is the ONE owned natural-exit predicate. Caller holds mu
// and has already observed done; neither guest claims nor Stop can satisfy it.
func (s *sandbox) cleanExitLocked() bool {
	return s.started && s.physicalExit && !s.forced && s.exit.Code == 0 && s.exit.Signal == "" && s.exit.Err == nil
}

func (s *sandbox) Destroy() error {
	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()
	s.mu.Lock()
	if s.destroyed {
		s.mu.Unlock()
		return nil
	}
	s.destroying = true
	started := s.started
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.destroying = false; s.mu.Unlock() }()
	if started {
		_ = s.Kill()
		s.Wait()
		if !s.refreshPhysicalExit() {
			return fmt.Errorf("the owned microvm may still run. Retain its disk and retry cleanup through this same controller")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root, err := cachefs.OpenPrivateRoot(s.spec.SessionDir, false)
	if err != nil {
		return err
	}
	defer root.Close()
	s.mu.Lock()
	lease := s.lease
	s.mu.Unlock()
	if lease == nil {
		lease, err = acquireVMLeaseFor(ctx, root, s.driver.kind)
		if err != nil {
			return err
		}
		defer lease.Close()
	}
	if err := jail.RemoveAll(ctx, vmDataDir(s.spec)); err != nil {
		return err
	}
	if err := root.Remove(s.driver.kind.name() + ".json"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove microvm specification: %w", err)
	}
	s.releaseLease()
	s.mu.Lock()
	s.destroyed = true
	s.mu.Unlock()
	return nil
}

func (s *sandbox) releaseLease() error {
	s.mu.Lock()
	lease := s.lease
	s.lease = nil
	s.mu.Unlock()
	if lease != nil {
		return lease.Close()
	}
	return nil
}

func (s *sandbox) releaseAgentsLease() error {
	s.mu.Lock()
	lease := s.agentsLease
	s.agentsLease = nil
	s.mu.Unlock()
	if lease != nil {
		return lease.Close()
	}
	return nil
}

func vmSocketPaths(dir string) (string, string) {
	return filepath.Join(dir, "api.sock"), filepath.Join(dir, "vsock.sock")
}
