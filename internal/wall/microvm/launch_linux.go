//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/jail"
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

func (s *sandbox) launchRunner(ctx context.Context, files *bootFiles, subuid bool, until time.Time) (net.Conn, *runnerControl, *jail.Cmd, error) {
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
	bootSpec := wall.Spec{ID: s.spec.ID, SessionDir: s.spec.SessionDir, MemoryMax: s.spec.MemoryMax, CPUs: s.spec.CPUs, DiskMax: s.spec.DiskMax, VM: s.spec.VM}
	plan := runnerPlan{Version: 1, Spec: bootSpec, BootUntil: until, SessionDirect: direct, UIDMode: s.uid}
	inherited := []*os.File{files.vmm, files.kernel, files.tools, files.image, files.agents, files.helper, rawChild, controlChild}
	grants := []jail.FileRuleV1{{FD: 3, Role: jail.FileProgramV1, Mode: 0o111, Access: jail.Read | jail.Exec}}
	for fd := 4; fd <= 7; fd++ {
		grants = append(grants, jail.FileRuleV1{FD: fd, Role: jail.FileDataV1, Mode: 0o444, Access: jail.Read})
	}
	command, err := jail.Command(ctx, jail.Spec{Argv: []string{"/proc/self/fd/8", "chv-run"}, Files: inherited, FileRulesV1: grants, Subuid: subuid, Net: jail.NetNone, Seccomp: jail.ProfileVMM, Binds: []jail.Bind{{Path: vmDataDir(s.spec), Write: true}, {Path: "/dev/kvm", Write: true}, {Path: "/dev/urandom"}}, Rules: []jail.PathRule{{Path: vmDataDir(s.spec), Access: jail.Connect}}})
	if err != nil {
		return nil, nil, nil, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	defer stdin.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open bounded VMM diagnostic pipe: %w", err)
	}
	defer writer.Close()
	command.Stdout = writer
	command.Stderr = writer
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
	if err := json.NewEncoder(stdin).Encode(plan); err != nil {
		stdin.Close()
		command.Signal(os.Kill)
		// The sandbox retains and reaps this exact child, including a failed
		// native wait. A startup error must not discard a live owner.
		return nil, nil, command, fmt.Errorf("send VMM runner plan: %w", err)
	}
	stdin.Close()
	failed = false
	return raw, newRunnerControl(control), command, nil
}
