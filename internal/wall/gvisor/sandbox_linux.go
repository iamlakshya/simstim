//go:build linux

package gvisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	if s.started || s.released || s.destroyed {
		s.mu.Unlock()
		return nil, failure("wall_setup", "this gvisor sandbox cannot start again", nil)
	}
	s.started = true
	s.mu.Unlock()
	fail := func(cause error) (net.Conn, error) {
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
	runtime, err := s.driver.openRuntime(ctx)
	if err != nil {
		return fail(err)
	}
	s.runtime = runtime
	if runtime == nil || runtime.program == nil || runtime.item.Kind != supply.KindGvisor || runtime.item.Name != "runsc" {
		return fail(errors.Join(fmt.Errorf("the protected gvisor runtime is incomplete"), runtime.Close()))
	}
	// The issuer checks the complete signed companion closure; this boundary
	// independently rechecks the exact open main executable's bytes.
	if err := supply.CheckOpenContext(ctx, runtime.item, runtime.program); err != nil {
		return fail(errors.Join(runtimeFileError("runsc", err), runtime.Close()))
	}
	s.version = runtime.item.Version
	plan, err := process.MountPlan(s.spec)
	if err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
	if err = s.storage.bundle(s.spec, plan, s.smoke); err != nil {
		return fail(errors.Join(err, runtime.Close()))
	}
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
	binds := []jail.Bind{{Path: filepath.Join(s.spec.SessionDir, "gvisor", "bundle")}, {Path: filepath.Join(s.spec.SessionDir, "gvisor", "runsc"), Write: true}}
	for _, m := range plan {
		binds = append(binds, jail.Bind{Path: m.Host, Write: m.Write})
	}
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
	commandSpec := jail.Spec{Argv: runArguments(filepath.Join(runtimeRoot(), "runsc"), s.spec.SessionDir, s.spec.ID, 3), Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC"}, Net: jail.NetNone, Files: []*os.File{theirs}, Binds: binds}
	command, err := s.driver.options.Command(execution, CommandRequest{Spec: commandSpec, Mounts: plan, Executables: runtime.executables, Log: log})
	if err == nil && command == nil {
		err = fmt.Errorf("gvisor namespace owner returned no owned command")
	}
	if err != nil {
		cancel()
		conn.Close()
		return fail(errors.Join(err, runtime.Close()))
	}
	s.mu.Lock()
	s.command, s.cancel, s.connection = command, cancel, conn
	s.mu.Unlock()
	s.mu.Lock()
	forced := s.forced
	s.mu.Unlock()
	if forced {
		cancel()
	}
	if err = command.Start(); err != nil {
		cancel()
		conn.Close()
		s.mu.Lock()
		s.physical = command.PhysicalExitV1()
		s.mu.Unlock()
		if s.physical {
			err = errors.Join(err, runtime.Close())
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

func (s *sandbox) reap(command OwnedCommand) {
	err := command.Wait()
	physical := command.PhysicalExitV1()
	if !physical {
		err = errors.Join(err, fmt.Errorf("gvisor namespace init exit remains uncertain"))
	}
	if physical {
		err = errors.Join(err, s.runtime.Close())
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
	defer s.mu.Unlock()
	if !s.started || s.physical {
		return nil
	}
	s.forced = true
	if s.connection != nil {
		_ = s.connection.Close()
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.command != nil {
		err := s.command.Signal(os.Kill)
		if s.command.PhysicalExitV1() {
			s.physical = true
			s.exit.Err = errors.Join(s.exit.Err, s.runtime.Close())
		}
		return errors.Join(err, s.exit.Err)
	}
	return nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started && !s.physical && s.command != nil && s.command.PhysicalExitV1() {
		s.physical = true
		s.exit.Err = errors.Join(s.exit.Err, s.runtime.Close())
	}
	if s.started && !s.physical {
		return failure("wall_setup", "the live or uncertain gvisor init still owns its prepared resources", nil)
	}
	s.released = true
	return s.storage.release()
}

func (s *sandbox) Destroy() error {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	if s.started && !s.physical && s.command != nil && s.command.PhysicalExitV1() {
		s.physical = true
		s.exit.Err = errors.Join(s.exit.Err, s.runtime.Close())
	}
	if s.destroyed {
		s.mu.Unlock()
		return nil
	}
	if s.started && !s.physical {
		s.mu.Unlock()
		return failure("wall_setup", "stop and prove gvisor init exit before destroying runtime files", nil)
	}
	s.released = true
	s.mu.Unlock()
	if err := s.storage.destroy(); err != nil {
		return failure("wall_setup", "cannot remove owned gvisor runtime files", err)
	}
	s.mu.Lock()
	s.destroyed = true
	s.mu.Unlock()
	return nil
}
