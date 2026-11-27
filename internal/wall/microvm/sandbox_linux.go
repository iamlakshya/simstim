//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/wall"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type sandbox struct {
	retirementMu           sync.Mutex
	retirements            map[*os.File]*microVMFileRetirementV2
	retirementDiagnostic   error
	runtimeLeaseDiagnostic error // bounded actual original phase/lease report
	bootFileDiagnostic     error // report-only snapshot from the SAME boot-file owner
	preparationClosing     bool
	dataRemoved            bool
	bootCustody            *bootFileCustodyV2
	driver                 *Driver
	spec                   wall.Spec
	uid                    string
	owner                  context.Context
	lease                  *os.File
	leaseMu                sync.Mutex
	leaseCloseErr          error
	preparedRoot           *os.Root
	rootCloseErr           error
	preparationErr         error
	preparedFiles          []*os.File
	fileCloseErr           error
	agentsLease            io.Closer
	agentsCloseMu          sync.Mutex
	mu                     sync.Mutex
	destroyMu              sync.Mutex
	reapMu                 sync.Mutex
	started, destroyed     bool
	destroying             bool
	preparedReleased       bool
	forced                 bool
	command                ownedVMProcess
	physicalExit           bool
	cancel                 context.CancelFunc
	connection             net.Conn
	control                *runnerControl
	done                   chan struct{}
	exit                   wall.Exit
	log                    byteTail
	logDone                chan struct{}
	helloChecked           bool
	bootFacts              api.CapProbe
	snapshotDisk           *snapshotDiskOwnerV1
	partialSlotOwners      []interface{ Close() error }
}

var _ wall.Driver = (*Driver)(nil)
var _ wall.Sandbox = (*sandbox)(nil)

func newSandbox(driver *Driver, spec wall.Spec, lease *os.File, mode string, owner context.Context) *sandbox {
	return &sandbox{driver: driver, spec: cloneBindingSpec(spec), lease: lease, uid: mode, owner: owner, done: make(chan struct{}), exit: wall.Exit{Code: -1}}
}

func (s *sandbox) Start(ctx context.Context) (_ net.Conn, returned error) {
	s.mu.Lock()
	if s.preparationErr != nil {
		err := s.preparationErr
		s.mu.Unlock()
		return nil, err
	}
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
	usable, _ := s.driver.kind.deviceAccess(facts)
	if !facts.UserNS || !usable || !facts.Seccomp || facts.LandlockABI < 1 || !api.SupportsMicroVMKernel(s.driver.Name(), facts.Kernel) {
		return nil, s.startFailed(fmt.Errorf("the microvm needs a supported host kernel, namespace jail, seccomp, Landlock and usable VMM device access"))
	}
	if err := noDumpPrerequisite(); err != nil {
		return nil, s.startFailed(err)
	}
	if !jail.Userns() {
		return nil, s.startFailed(fmt.Errorf("the VMM requires the canonical namespace jail; its empty network namespace is unavailable"))
	}
	if s.uid == slotUIDModeV1Name {
		if !slotAccountModeV1(facts) {
			return nil, s.startFailed(fmt.Errorf("the saved own-account mode has no current optional root producer"))
		}
		return s.startSlotV1(ctx, facts)
	}
	if !(s.driver.kind == nitroBackend && s.uid == "login") && s.uid != uidMode(facts) {
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
	if adoptErr := s.adoptBootFilesV2(files); adoptErr != nil {
		return nil, s.startFailed(errors.Join(err, adoptErr))
	}
	if err != nil {
		return nil, s.startFailed(err)
	}
	defer s.finishBootFilesAtReturnV2(&returned)
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
		return nil, s.bootFailure(fmt.Errorf("guest connection was not ready: %w", err))
	}
	checked, err := link.ReadGuestHello(boot, conn)
	if err != nil {
		s.Kill()
		s.Wait()
		return nil, s.bootFailure(fmt.Errorf("guest init did not boot within its deadline: %w", err))
	}
	stopStartupCancellation()
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
	snapshotErr := s.closeSnapshotDiskV1()
	err = errors.Join(err, snapshotErr)
	if snapshotErr == nil {
		err = errors.Join(err, s.releaseBootStackV2())
	}
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

func (s *sandbox) releaseAgentsLease() error {
	s.agentsCloseMu.Lock()
	defer s.agentsCloseMu.Unlock()
	s.mu.Lock()
	lease := s.agentsLease
	s.mu.Unlock()
	if lease == nil {
		return nil
	}
	// Close may need the original native creation to settle. Keep that exact
	// owner on failure, and never hold the main mutex across its physical join.
	err := lease.Close()
	s.captureRuntimeLeaseDiagnosticsV4(lease)
	if err != nil {
		return err
	}
	s.mu.Lock()
	// Both Start paths install this field once; a second Start is refused.
	// This serialised release is its only clearing writer.
	s.agentsLease = nil
	s.mu.Unlock()
	return nil
}

func vmSocketPaths(dir string) (string, string) {
	return filepath.Join(dir, "api.sock"), filepath.Join(dir, "vsock.sock")
}
