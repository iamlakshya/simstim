//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/nitrodev"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

type runnerPlan struct {
	Version       int       `json:"version"`
	Spec          wall.Spec `json:"spec"`
	BootUntil     time.Time `json:"boot_until"`
	SessionDirect bool      `json:"session_direct"`
	UIDMode       string    `json:"uid_mode"`
}

func socketPair() (net.Conn, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open private VMM socket pair: %w", err)
	}
	ours := os.NewFile(uintptr(fds[0]), "vmm-parent")
	theirs := os.NewFile(uintptr(fds[1]), "vmm-child")
	conn, err := net.FileConn(ours)
	ours.Close()
	if err != nil {
		theirs.Close()
		return nil, nil, fmt.Errorf("use private VMM socket pair: %w", err)
	}
	return conn, theirs, nil
}

func (s *sandbox) launchRunner(ctx context.Context, files *bootFiles, subuid bool, until time.Time) (rawResult net.Conn, controlResult *runnerControl, commandResult *jail.Cmd, returned error) {
	if files.vmmOwner == nil {
		return nil, nil, nil, fmt.Errorf("VMM launch lost its original verified runtime owner")
	}
	modeSource, err := files.vmmOwner.BeginExecMode(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	readonly := false
	if modeSource != nil {
		readonly, err = modeSource.ReadonlyOriginalV1(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	s.mu.Lock()
	owner := s.snapshotDisk
	s.mu.Unlock()
	if owner == nil {
		return nil, nil, nil, fmt.Errorf("VMM launch lost its actual original disk owner")
	}
	if readonly {
		// This is the SAME pre-chown parent descriptor, never S opening the
		// subordinate disk. B's genuine native receive supplies that disk.
		return s.launchRunnerWithDiskV1(ctx, files, owner.diskParent, subuid, until, modeSource)
	}
	if err := owner.capture(ctx); err != nil {
		return nil, nil, nil, err
	}
	returned = owner.borrow(ctx, func(_ *os.Root, _ *os.Root, disk *os.File) error {
		var err error
		rawResult, controlResult, commandResult, err = s.launchRunnerWithDiskV1(ctx, files, disk, subuid, until, modeSource)
		return err
	})
	return
}

func (s *sandbox) launchRunnerWithDiskV1(ctx context.Context, files *bootFiles, disk *os.File, subuid bool, until time.Time, modeSource *supply.ExecModeLeaseV1) (rawResult net.Conn, controlResult *runnerControl, commandResult *jail.Cmd, returned error) {
	raw, rawChild, err := socketPair()
	if err != nil {
		return nil, nil, nil, err
	}
	control, controlChild, err := socketPair()
	if err != nil {
		raw.Close()
		rawChild.Close()
		return nil, nil, nil, err
	}
	failed := true
	defer func() {
		rawChild.Close()
		controlChild.Close()
		if failed {
			raw.Close()
			control.Close()
		}
	}()
	direct, err := sessionDirect(s.spec)
	if err != nil {
		return nil, nil, nil, err
	}
	bootSpec := wall.Spec{Driver: s.driver.Name(), ID: s.spec.ID, SessionDir: s.spec.SessionDir, MemoryMax: s.spec.MemoryMax, CPUs: s.spec.CPUs, DiskMax: s.spec.DiskMax, VM: s.spec.VM}
	plan := runnerPlan{Version: 1, Spec: bootSpec, BootUntil: until, SessionDirect: direct, UIDMode: s.uid}
	controller, err := newNativeRunnerControl(control, bootSpec)
	if err != nil {
		return nil, nil, nil, err
	}
	var config *protectedBootConfig
	if s.driver.kind.isFirecracker() {
		config, err = prepareFirecrackerConfig(ctx, s.spec)
		files.config = config // Adopt the actual partial owner BEFORE handling error.
		if err != nil {
			return nil, nil, nil, err
		}
	}
	var modePhase *vmmExecPhaseV1
	inherited := runnerFilesWithDiskV1(files, disk, rawChild, controlChild)
	programMode := os.FileMode(0o111)
	readonly := false
	if modeSource != nil {
		programMode = 0o555
		plan.Version = 2
		readonly, err = modeSource.ReadonlyOriginalV1(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		if readonly {
			plan.Version = 3
		}
	}
	grants := []jail.FileRuleV1{{FD: 3, Role: jail.FileProgramV1, Mode: programMode, Access: jail.Read | jail.Exec}}
	for fd := 4; fd <= 7; fd++ {
		grants = append(grants, jail.FileRuleV1{FD: fd, Role: jail.FileDataV1, Mode: 0o444, Access: jail.Read})
	}
	if !readonly {
		grants = append(grants, jail.FileRuleV1{FD: 11, Role: jail.FileDataV1, Mode: 0o600, Access: jail.Read | jail.Write})
	}
	binds := []jail.Bind{{Path: vmDataDir(s.spec), Write: true}, {Path: "/dev/urandom"}}
	if s.driver.kind == nitroBackend {
		device, err := nitrodev.Open()
		if err != nil {
			return nil, nil, nil, err
		}
		defer device.Close()
		binds = append(binds, jail.Bind{File: device, Path: nitrodev.Device, Write: true})
	} else {
		binds = append(binds, jail.Bind{Path: "/dev/kvm", Write: true})
	}
	if config != nil {
		if err := config.check(); err != nil {
			return nil, nil, nil, err
		}
		binds = append(binds, jail.Bind{Path: config.path})
	}
	command, err := jail.Command(ctx, jail.Spec{Argv: []string{"/proc/self/fd/8", "chv-run"}, Files: inherited, FileRulesV1: grants, Subuid: subuid, Net: jail.NetNone, Seccomp: jail.ProfileVMM, Binds: binds, Rules: []jail.PathRule{{Path: vmDataDir(s.spec), Access: jail.Connect}}})
	if err != nil {
		return nil, nil, nil, err
	}
	started := false
	defer func() {
		if !started && modePhase != nil {
			var settleErr error
			if modePhase.attached {
				settleErr = command.SettleVMMUnstartedV1(modeSource)
			} else {
				settleErr = command.DisableUnstartedVMMV1()
			}
			if settleErr != nil {
				commandResult = command
				returned = errors.Join(returned, settleErr)
			}
		}
	}()
	if modeSource != nil {
		modePhase, err = newVMMExecPhaseV1(ctx, command, modeSource, files.vmm, control, s.spec.ID, s.driver.kind.name())
		if modePhase != nil {
			modePhase.snapshotOwner = s.snapshotDisk
			if retainErr := s.retainVMMModePhaseV1(modePhase); retainErr != nil {
				return nil, nil, command, errors.Join(err, retainErr)
			}
		}
		if err != nil {
			return nil, nil, command, err
		}
		if readonly {
			original, err := readonlyInitPlanV1(plan)
			if err != nil {
				return nil, nil, nil, err
			}
			if err := command.BindVMMReadonlyInitPlanV1(modeSource, original); err != nil {
				return nil, nil, nil, err
			}
		}
		if err := controller.attachModePhaseV1(modePhase); err != nil {
			return nil, nil, nil, err
		}
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	defer stdin.Close()
	var origin *localLaunchOriginV1
	if !readonly {
		origin, err = s.captureLocalLaunchOriginV1(ctx, plan, files, disk, command, stdin, controller, config)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open bounded VMM diagnostic pipe: %w", err)
	}
	defer writer.Close()
	command.Stdout = writer
	command.Stderr = writer
	if err := s.attachBootFileCommandV2(command); err != nil {
		return nil, nil, nil, err
	}
	started = true // From here ONLY actual Cmd proof may settle creation.
	if err := command.Start(); err != nil {
		reader.Close()
		if !command.PhysicalExitV1() {
			return nil, nil, command, err
		}
		return nil, nil, nil, err
	}
	writer.Close()
	s.logDone = make(chan struct{})
	go func() { defer close(s.logDone); _, _ = io.Copy(&s.log, reader); reader.Close() }()
	var planErr error
	if readonly {
		planErr = json.NewEncoder(stdin).Encode(plan)
	} else {
		planErr = origin.sendPlanV1()
	}
	if err := planErr; err != nil {
		stdin.Close()
		command.Signal(os.Kill)
		// The sandbox retains and reaps this exact child, including a failed
		// native wait. A startup error must not discard a live owner.
		return nil, nil, command, fmt.Errorf("send VMM runner plan: %w", err)
	}
	stdin.Close()
	if modePhase != nil {
		phaseContext := ctx
		if readonly {
			var stop context.CancelFunc
			phaseContext, stop = context.WithDeadline(ctx, until)
			defer stop()
		}
		if err := modePhase.run(phaseContext, controller); err != nil {
			_ = command.Signal(os.Kill)
			// SAME actual Cmd/source retains an uncertain armed fence until
			// original physical whole-source cleanup proves no future exec.
			return nil, nil, command, fmt.Errorf("complete original VMM exec mode phase: %w", err)
		}
	}
	failed = false
	return raw, controller, command, nil
}

// The existing endpoints remain at9/10. The final slot carries the disk that
// the same factory retained; the jailed runner may never reopen it by path.
func runnerFilesWithDiskV1(files *bootFiles, disk, raw, control *os.File) []*os.File {
	return []*os.File{files.vmm, files.kernel, files.tools, files.image, files.agents, files.helper, raw, control, disk}
}
