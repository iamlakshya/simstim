//go:build linux

package gvisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
	"golang.org/x/sys/unix"
)

func (s *sandbox) Start(ctx context.Context) (net.Conn, error) {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	if s.preparationErr != nil {
		err := s.preparationErr
		s.mu.Unlock()
		return nil, err
	}
	if s.started || s.released || s.destroyed {
		s.mu.Unlock()
		return nil, failure("wall_setup", "this gvisor sandbox cannot start again", nil)
	}
	s.started = true
	s.mu.Unlock()
	fail := func(cause error) (net.Conn, error) {
		if s.command == nil {
			cause = errors.Join(cause, s.topology.Close())
		}
		s.mu.Lock()
		s.exit = wall.Exit{Code: -1, Err: cause}
		if s.command == nil {
			s.physical = true
		}
		close(s.done)
		s.mu.Unlock()
		code := "wall_setup"
		var supplied *proto.Error
		if errors.As(cause, &supplied) && (supplied.Code == "runtime_missing" || supplied.Code == "runtime_advisory") {
			code = supplied.Code
		}
		return nil, failure(code, "cannot start gvisor", cause)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	runtime, err := s.driver.openRuntime(s.owner)
	s.runtime = runtime // The actual lower owner survives even its error.
	if err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	if runtime == nil || runtime.program == nil || runtime.item.Kind != supply.KindGvisor || runtime.item.Name != "runsc" {
		return fail(errors.Join(fmt.Errorf("the protected gvisor runtime is incomplete"), runtime.Close()))
	}
	// The issuer checks the complete signed companion closure; this boundary
	// independently rechecks the exact open main executable's bytes.
	if err := supply.CheckSealedRuntime(runtime.item, runtime.program); err != nil {
		return fail(errors.Join(runtimeFileError("runsc", err), runtime.Close()))
	}
	s.version = runtime.item.Version
	plan, err := process.MountPlan(s.spec)
	if err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	plan, mountFiles, err := process.CaptureMountPlanV1(ctx, plan)
	if err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	defer func() {
		for _, file := range mountFiles {
			_ = file.Close()
		}
	}()
	forkMounts, err := captureForkMountsV1(s, plan, mountFiles)
	s.mu.Lock()
	s.forkMounts = forkMounts
	s.mu.Unlock()
	if err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	if err = s.storage.bundle(s.spec, plan, mountFiles, s.smoke, forkMounts); err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	s.topology, err = freezeNativeTopologyV1(s.spec, plan, mountFiles, s.smoke)
	if err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	storageBinds, err := s.storage.commandBindsV1()
	if err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	defer func() {
		for _, bound := range storageBinds {
			_ = bound.File.Close()
		}
	}()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	ours, theirs := os.NewFile(uintptr(fds[0]), "gvisor-supervisor-link"), os.NewFile(uintptr(fds[1]), "gvisor-guest-link")
	defer theirs.Close()
	conn, err := net.FileConn(ours)
	closeErr := ours.Close()
	if err != nil || closeErr != nil {
		if conn != nil {
			conn.Close()
		}
		return fail(errors.Join(err, closeErr, runtime.Close()))
	}
	log, err := s.storage.log()
	if err != nil {
		conn.Close()
		return fail(errors.Join(err, runtime.Close()))
	}
	defer log.Close()
	execution, cancel := context.WithCancel(s.owner)
	interrupted := make(chan struct{})
	stopStartup := context.AfterFunc(ctx, func() { cancel(); close(interrupted) })
	var startupOnce sync.Once
	joinStartup := func() {
		startupOnce.Do(func() {
			if !stopStartup() {
				<-interrupted
			}
		})
	}
	defer joinStartup()
	commandSpec := jail.Spec{Argv: CommandArgumentsV1(s.spec.SessionDir, s.spec.ID), Env: CommandEnvironmentV1(), Net: jail.NetNone, Files: []*os.File{theirs}, Binds: storageBinds}
	command, err := s.driver.options.Command(execution, CommandRequest{ForkMounts: forkMounts, Topology: s.topology, Suite: runtime.protectedSuite, Spec: commandSpec, Mounts: plan, MountFiles: mountFiles, Executables: runtime.executables, Log: log})
	if err == nil && command == nil {
		err = fmt.Errorf("gvisor namespace owner returned no owned command")
	}
	if command != nil {
		s.mu.Lock()
		s.command, s.cancel, s.connection = command, cancel, conn
		s.mu.Unlock()
	}
	if err != nil {
		cancel()
		conn.Close()
		if command != nil {
			err = errors.Join(err, s.releaseCommandV1())
			physical := command.PhysicalExitV1()
			s.mu.Lock()
			s.physical = physical
			s.mu.Unlock()
			if !physical {
				return fail(err)
			}
		}
		return fail(errors.Join(err, runtime.Close()))
	}
	s.mu.Lock()
	forced := s.forced
	s.mu.Unlock()
	if forced {
		cancel()
	}
	if err = command.Start(); err != nil {
		cancel()
		conn.Close()
		physical := command.PhysicalExitV1()
		s.mu.Lock()
		s.physical = physical
		s.mu.Unlock()
		if physical {
			err = errors.Join(err, runtime.Close(), s.topology.Close())
		}
		return fail(err)
	}
	go s.reap(command)
	if s.smoke {
		return conn, nil
	}
	ready, cancelReady := context.WithTimeout(ctx, 10*time.Second)
	defer cancelReady()
	checked, err := link.ReadGuestHello(ready, conn)
	if err != nil {
		killErr := s.Kill()
		result := s.Wait()
		return nil, failure("wall_unavailable", "gvisor init did not become ready", errors.Join(err, killErr, result.Err))
	}
	joinStartup()
	if err := ctx.Err(); err != nil {
		killErr := s.Kill()
		result := s.Wait()
		return nil, errors.Join(err, killErr, result.Err)
	}
	s.mu.Lock()
	s.connection = checked
	s.hello = true
	s.mu.Unlock()
	return checked, nil
}

// FreezeV1 freezes the actual live gvisor command's complete native cgroup.
func (s *sandbox) FreezeV1(ctx context.Context, maxMS int) error {
	s.mu.Lock()
	owner := s.nativeLive
	s.mu.Unlock()
	return owner.FreezeV1(ctx, maxMS)
}

// ThawV1 ends only the same original native command's owned freeze lease.
func (s *sandbox) ThawV1() error {
	s.mu.Lock()
	owner := s.nativeLive
	s.mu.Unlock()
	return owner.ThawV1()
}

// FreezeReasonV1 reports the actual unavailable native capability.
func (s *sandbox) FreezeReasonV1() string {
	s.mu.Lock()
	owner := s.nativeLive
	s.mu.Unlock()
	return owner.FreezeReasonV1()
}

func (s *sandbox) reap(command OwnedCommand) {
	err := command.Wait()
	physical := command.PhysicalExitV1()
	if !physical {
		err = errors.Join(err, fmt.Errorf("gvisor namespace init exit remains uncertain"))
	}
	if physical {
		err = errors.Join(err, s.runtime.Close(), s.topology.Close())
	}
	s.mu.Lock()
	s.physical = physical
	s.exit = wall.Exit{Code: command.ExitCode(), Err: err}
	var exit *jail.ExitError
	if errors.As(err, &exit) && exit.Signal != 0 {
		s.exit.Signal = exit.Signal.String()
	}
	close(s.done)
	s.mu.Unlock()
}

func (s *sandbox) Kill() error {
	s.mu.Lock()
	if !s.started || s.physical {
		s.mu.Unlock()
		return nil
	}
	s.forced = true
	connection, cancel, command := s.connection, s.cancel, s.command
	s.mu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
	if cancel != nil {
		cancel()
	}
	var signalErr error
	if command != nil {
		signalErr = command.Signal(os.Kill)
		s.observeGvisorPhysicalExitV1(command)
	}
	s.mu.Lock()
	exitErr := s.exit.Err
	s.mu.Unlock()
	return errors.Join(signalErr, exitErr)
}

// Native closure joins original fork borrowers. No sandbox mutex may be held:
// each borrower checks this SAME sandbox before and after its actual file copy.
func (s *sandbox) observeGvisorPhysicalExitV1(command OwnedCommand) {
	if command == nil || !command.PhysicalExitV1() {
		return
	}
	runtimeErr := errors.Join(s.runtime.Close(), s.topology.Close())
	s.mu.Lock()
	s.physical = true
	s.exit.Err = errors.Join(s.exit.Err, runtimeErr)
	s.mu.Unlock()
}

func (s *sandbox) Stop(ctx context.Context, grace time.Duration) error {
	s.mu.Lock()
	command, started := s.command, s.started
	s.mu.Unlock()
	if !started {
		return nil
	}
	var signalErr error
	if command != nil {
		signalErr = command.Signal(syscall.SIGTERM)
	}
	timer := time.NewTimer(max(grace, 0))
	defer timer.Stop()
	select {
	case <-s.done:
		return errors.Join(signalErr, s.Wait().Err)
	case <-ctx.Done():
		return errors.Join(ctx.Err(), signalErr, s.Kill())
	case <-timer.C:
		return errors.Join(signalErr, s.Kill())
	}
}

func (s *sandbox) Wait() wall.Exit {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return wall.Exit{Code: -1, Err: fmt.Errorf("gvisor was not started")}
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

func (s *sandbox) ReleasePreparedV1() error {
	s.life.Lock()
	defer s.life.Unlock()
	s.refreshPhysicalV1()
	s.mu.Lock()
	command := s.command
	s.mu.Unlock()
	s.observeGvisorPhysicalExitV1(command)
	s.mu.Lock()
	if s.started && !s.physical {
		s.mu.Unlock()
		return failure("wall_setup", "the live or uncertain gvisor init still owns its prepared resources", nil)
	}
	fork := s.forkMounts
	s.mu.Unlock()
	if fork != nil {
		if err := fork.close(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.released = true
	s.mu.Unlock()
	return errors.Join(s.releaseCommandV1(), s.runtime.Close(), s.topology.Close(), s.storage.release())
}

func (s *sandbox) Destroy() error {
	s.life.Lock()
	defer s.life.Unlock()
	s.refreshPhysicalV1()
	s.mu.Lock()
	if s.destroyed {
		s.mu.Unlock()
		return nil
	}
	command := s.command
	s.mu.Unlock()
	s.observeGvisorPhysicalExitV1(command)
	s.mu.Lock()
	if s.started && !s.physical {
		s.mu.Unlock()
		return failure("wall_setup", "stop and prove gvisor init exit before destroying runtime files", nil)
	}
	fork := s.forkMounts
	s.mu.Unlock()
	if fork != nil {
		if err := fork.close(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.released = true
	s.mu.Unlock()
	if err := errors.Join(s.releaseCommandV1(), s.runtime.Close(), s.topology.Close()); err != nil {
		return err
	}
	if err := s.storage.destroy(); err != nil {
		return failure("wall_setup", "cannot remove owned gvisor runtime files", err)
	}
	s.mu.Lock()
	s.destroyed = true
	s.mu.Unlock()
	return nil
}

// Native completion may join root/copy borrowers that recheck this sandbox.
// Neither the physical poll nor owner retirement runs while holding s.mu.
func (s *sandbox) refreshPhysicalV1() {
	s.mu.Lock()
	command, pending := s.command, s.started && !s.physical
	s.mu.Unlock()
	if !pending || command == nil || !command.PhysicalExitV1() {
		return
	}
	s.mu.Lock()
	s.physical = true
	s.mu.Unlock()
}

// The actual command must retain and release its own code/native preparation.
// A physical process result alone does not clear an earlier descriptor error.
func (s *sandbox) releaseCommandV1() error {
	if s.command == nil {
		return nil
	}
	owner, ok := s.command.(interface{ ReleasePreparedV1() error })
	if !ok {
		return failure("wall_setup", "the actual gvisor command has no prepared-resource release", nil)
	}
	return owner.ReleasePreparedV1()
}
