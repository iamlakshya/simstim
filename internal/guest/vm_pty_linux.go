//go:build linux

package guest

import (
	"errors"
	"io"
	"os"
	"sort"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/guestpty"
	"github.com/iamlakshya/simstim/internal/proto"
)

type vmPTY struct {
	session, token string
	channel        *guestpty.Channel
	run            *execRun
	starting       bool
	master         *os.File
	done           chan struct{}
	outputDone     chan struct{}
	inputDone      chan struct{}
	outputErr      error
}

type vmPTYWriter struct{ channel *guestpty.Channel }

func (w vmPTYWriter) Write(data []byte) (int, error) {
	if err := w.channel.Output(data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func vmAgentEnvironment(a proto.CtlStartArgs, docker bool) []string {
	env := append([]string(nil), a.Env...)
	if docker {
		env = append(env, "DOCKER_HOST=unix:///run/"+brand.Name+"/docker.sock")
	}
	keys := make([]string, 0, len(a.Secrets))
	for key := range a.Secrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+a.Secrets[key])
	}
	return env
}

func (v *vmRuntime) validateMember(a proto.CtlStartArgs, primary bool) error {
	if !v.prepared() {
		return vmError("the VM member cannot start before preparation")
	}
	if len(a.RunToken) == 0 || len(a.RunToken) > 128 {
		return vmError("the VM member has no valid run capability")
	}
	for _, value := range []byte(a.RunToken) {
		if value < 0x21 || value > 0x7e {
			return vmError("the VM member run capability is invalid")
		}
	}
	if a.DNS != "" && a.DNS != proto.DNSSynthetic {
		return vmError("the VM member cannot replace synthetic DNS mode")
	}
	if primary {
		if err := v.validateSession(a); err != nil {
			return err
		}
	} else if a.CaptureNonce != "" || len(a.StateFiles) != 0 {
		return vmError("a secondary VM member cannot replace the prepared state plan")
	}
	return nil
}

func (v *vmRuntime) startAgent(a proto.CtlStartArgs, hp *holderProc) (int, error) {
	v.mu.Lock()
	terminal := v.ptys[a.Session]
	if terminal == nil || terminal.token != a.RunToken || terminal.run != nil || terminal.starting {
		v.mu.Unlock()
		return 0, vmError("the VM holder has no matching fresh PTY capability")
	}
	terminal.starting = true
	v.mu.Unlock()
	defer func() { v.mu.Lock(); terminal.starting = false; v.mu.Unlock() }()

	if len(a.Argv) == 0 || a.Argv[0] == "" {
		return 0, vmError("the VM agent command is empty")
	}
	run := newOwnedCommand(v.ctx, proto.CtlExecArgs{ID: a.Session, Argv: a.Argv, Cwd: a.Cwd}, vmAgentEnvironment(a, v.plan.Args.VM.Docker))
	command, _, err := v.init.agentCommand(run, nil, nil)
	if err != nil {
		run.cancel()
		return 0, err
	}
	master, slave, err := pty.Open()
	if err != nil {
		run.cancel()
		return 0, err
	}
	cols, rows := a.Cols, a.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if cols > 65535 {
		cols = 65535
	}
	if rows > 65535 {
		rows = 65535
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		master.Close()
		slave.Close()
		run.cancel()
		return 0, err
	}
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr.Setpgid = false
	command.SysProcAttr.Setsid = true
	command.SysProcAttr.Setctty = true
	command.SysProcAttr.Ctty = 0
	run.abortOutput = func() { _ = terminal.channel.Conn().SetWriteDeadline(time.Now()) }
	if err := v.init.startOwned(run, command); err != nil {
		master.Close()
		slave.Close()
		run.cancel()
		return 0, err
	}
	slave.Close()
	v.mu.Lock()
	terminal.master, terminal.run = master, run
	v.mu.Unlock()
	go terminal.input(v)
	go func() {
		defer close(terminal.outputDone)
		_, terminal.outputErr = io.Copy(vmPTYWriter{terminal.channel}, master)
	}()
	go terminal.finish(v)
	return command.Process.Pid, nil
}

func (p *vmPTY) input(v *vmRuntime) {
	defer close(p.inputDone)
	for {
		frame, err := p.channel.Read()
		if err != nil {
			p.run.cancel()
			_ = p.run.signal(uint32(syscall.SIGKILL))
			_ = v.memory.killMember(p.run.vmGroup)
			return
		}
		switch frame.Type {
		case proto.TInput:
			if _, err := p.master.Write(frame.Payload); err != nil {
				p.run.cancel()
				return
			}
		case proto.TResize:
			var size proto.Resize
			if guestpty.DecodeControl(frame.Payload, &size) != nil || size.Cols <= 0 || size.Rows <= 0 || size.Cols > 65535 || size.Rows > 65535 {
				p.run.cancel()
				return
			}
			_ = pty.Setsize(p.master, &pty.Winsize{Cols: uint16(size.Cols), Rows: uint16(size.Rows)})
		case proto.TRequest:
			var request struct {
				Signal uint32 `json:"signal"`
			}
			if guestpty.DecodeControl(frame.Payload, &request) != nil || !guestpty.AllowedSignal(request.Signal) {
				p.run.cancel()
				return
			}
			_ = p.run.signal(request.Signal)
		default:
			p.run.cancel()
			return
		}
	}
}

func (p *vmPTY) finish(v *vmRuntime) {
	defer close(p.done)
	stopped := make(chan struct{})
	go func() {
		select {
		case <-p.run.ctx.Done():
			_ = p.run.signal(uint32(syscall.SIGKILL))
			_ = v.memory.killMember(p.run.vmGroup)
			_ = p.channel.Conn().SetWriteDeadline(time.Now().Add(time.Second))
		case <-stopped:
		}
	}()
	known, err := v.init.waitOwned(p.run)
	close(stopped)
	_ = p.channel.Conn().SetWriteDeadline(time.Now().Add(time.Second))
	select {
	case <-p.outputDone:
	case <-time.After(time.Second):
		v.setError(vmError("the owned VM PTY did not drain after child cleanup"))
		p.channel.Close()
		p.master.Close()
		<-p.outputDone
	}
	p.master.Close()
	result := proto.Ended{ExitCode: execWaitCode(err)}
	if !known {
		result.ExitCode = 125
		v.setError(vmError("the VM agent's owned exit evidence was lost"))
	}
	var ended *os.ProcessState
	if p.run.cmd != nil {
		ended = p.run.cmd.ProcessState
	}
	if ended != nil {
		if status, ok := ended.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.Signal = status.Signal().String()
		}
	}
	if p.outputErr != nil && !errors.Is(p.outputErr, io.EOF) && !errors.Is(p.outputErr, syscall.EIO) {
		v.setError(p.outputErr)
	}
	if err := p.channel.End(result); err != nil {
		v.setError(err)
	}
	p.channel.Close()
	<-p.inputDone
}

func equalVMStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (v *vmRuntime) stopMember(id string) error {
	v.mu.Lock()
	p := v.ptys[id]
	var run *execRun
	if p != nil {
		run = p.run
	}
	v.mu.Unlock()
	if run == nil {
		return vmError("that session is not an owned VM PTY member")
	}
	_ = run.signal(uint32(syscall.SIGTERM))
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		run.cancel()
		_ = v.memory.killMember(run.vmGroup)
		<-p.done
	}
	v.init.mu.Lock()
	holder := v.init.members[id]
	v.init.mu.Unlock()
	if holder != nil {
		holder.stop(0)
		v.init.drainHolderEvents(holder)
	}
	v.init.endIfEmpty()
	return nil
}

func (v *vmRuntime) stop(grace time.Duration) {
	v.mu.Lock()
	var members []*vmPTY
	for _, p := range v.ptys {
		if p.run != nil {
			members = append(members, p)
		}
	}
	v.mu.Unlock()
	for _, p := range members {
		_ = p.run.signal(uint32(syscall.SIGTERM))
	}
	if grace < 0 {
		grace = 0
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	finished := make(chan struct{})
	go func() {
		for _, p := range members {
			<-p.done
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-deadline.C:
		for _, p := range members {
			p.run.cancel()
			_ = v.memory.killMember(p.run.vmGroup)
		}
		<-finished
	}
	v.init.end()
}

func (v *vmRuntime) joinPTY() bool {
	v.mu.Lock()
	var members []*vmPTY
	for _, p := range v.ptys {
		members = append(members, p)
	}
	v.mu.Unlock()
	for _, p := range members {
		v.mu.Lock()
		started := p.run != nil
		v.mu.Unlock()
		if started {
			<-p.done
		} else {
			p.channel.Close()
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.endErr == nil
}
