//go:build linux

package guest

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
)

type vmRuntime struct {
	connection        *vmInitConnection
	init              *Init
	files             *vmFiles
	state             *vmState
	memory            *vmMemory
	plan              vmPreparePlan
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	applied           bool
	preparing, failed bool
	prepareDone       chan struct{}
	ptys              map[string]*vmPTY
	network           *vmNetwork
	docker            *execRun
	endErr            error
	seedIO            map[*link.Stream]struct{}
	seedWG            sync.WaitGroup
	holderIO          map[*link.Stream]net.Conn
	holderWG          sync.WaitGroup
	controlWG         sync.WaitGroup
	nextGroup         uint64 // advanced only under the canonical Init.mu spawn lock
}

func runMicroVM() error {
	if err := verifyVMEntry(); err != nil {
		return err
	}
	if err := secretmem.Protect(); err != nil {
		return vmError("the VM init cannot disable its own core files and dumpability")
	}
	fs := newVMFiles("/", realVMMounts())
	if err := fs.bootstrap(); err != nil {
		_ = fs.close()
		return err
	}
	if err := verifyVMToolsRoot(); err != nil {
		_ = fs.close()
		return err
	}
	if err := verifyVMDisks(vmReadKernelFile); err != nil {
		_ = fs.close()
		return err
	}
	listener, err := listenVMGuest()
	if err != nil {
		_ = fs.close()
		return err
	}
	// No guest program exists yet. The host-owned VMM deadline bounds this
	// one link; there is no guest-to-host connect or trusted named listener.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer cancel()
	conn, err := acceptVMGuest(ctx, listener)
	listener.Close()
	if err != nil {
		_ = fs.close()
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	g := New(conn, "", log)
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	vm := &vmRuntime{init: g, files: fs, ctx: lifetime, cancel: stop, ptys: map[string]*vmPTY{}}
	vm.connection, err = newVMInitConnection(lifetime, g, conn)
	if err != nil {
		_ = g.sess.Close()
		_ = fs.close()
		return err
	}
	g.vm = vm
	g.reaper = becomeSubreaper(log)
	if !g.reaper {
		conn.Close()
		_ = fs.close()
		return vmError("the VM init cannot own and reap its descendants")
	}
	go func() {
		select {
		case <-ctx.Done():
			g.end()
		case <-g.done:
		}
	}()
	go g.reapLoop()
	g.Serve()
	vm.mu.Lock()
	failure := vm.endErr
	vm.mu.Unlock()
	if failure != nil {
		log.Error("VM PID1 remains failed until its owner ends the monitor", "err", failure)
		return parkVMFailure()
	}
	return nil
}

func (v *vmRuntime) prepare(a proto.CtlVMPrepareArgs) (proto.CtlVMPrepareResult, error) {
	if err := v.connection.check(v.init); err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	plan, err := validateVMPrepare(a)
	if err != nil {return proto.CtlVMPrepareResult{},err}
	if plan.Args.Correlation==nil {return proto.CtlVMPrepareResult{},vmError("the actual VM prepare has no original host correlation")}
	v.mu.Lock()
	if v.applied || v.preparing || v.failed {
		v.mu.Unlock()
		return proto.CtlVMPrepareResult{}, vmError("the VM root was already prepared or its preparation failed")
	}
	v.preparing = true
	v.prepareDone = make(chan struct{})
	v.mu.Unlock()
	complete := false
	defer func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.preparing = false
		if !complete {
			v.failed = true
		}
		close(v.prepareDone)
	}()
	if err := v.files.mountDisks(); err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	if err := v.files.overlay(a.VM.System, a.VM.Sudo); err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	if err := v.files.layout(plan.State, a.VM.Sudo || a.VM.Docker); err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	if err := v.files.accounts(a.VM.Sudo); err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	state, err := v.files.restoreFiles(plan)
	if err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	if err := v.files.pivotInto(); err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	memory, err := setupVMMemory(v.ctx, v.files.mountLive)
	if err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	v.plan, v.state, v.memory = plan, state, memory
	network, err := v.startNetwork()
	if err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	v.network = network
	if a.VM.Docker {
		if err := v.startDocker(); err != nil {
			return proto.CtlVMPrepareResult{}, err
		}
	}
	if err := v.connection.check(v.init); err != nil {
		return proto.CtlVMPrepareResult{}, err
	}
	v.mu.Lock()
	v.applied = true
	v.mu.Unlock()
	complete = true
	correlation:=*plan.Args.Correlation
	return proto.CtlVMPrepareResult{GuestABI:proto.GuestOwnedPTYV2,Correlation:&correlation},nil
}

func (v *vmRuntime) prepared() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.applied }
func (v *vmRuntime) ptyABI() string {
	if !v.prepared() {
		return ""
	}
	return proto.GuestOwnedPTYV2
}
func (v *vmRuntime) helperExecutable() (string, error) {
	if !v.prepared() {
		return "", vmError("the VM tools are not prepared")
	}
	return filepath.Join("/opt", brand.Name, "bin", brand.HostBinary), nil
}

func (v *vmRuntime) agentProcess() (*syscall.SysProcAttr, error) {
	if !v.prepared() {
		return nil, vmError("the VM has not applied its protected setup")
	}
	attr, err := agentProcess()
	if err != nil {
		return nil, err
	}
	return attr, nil
}
func (v *vmRuntime) holderProcess() (*syscall.SysProcAttr, error) {
	if !v.prepared() {
		return nil, vmError("the VM holder cannot run before prepare")
	}
	return &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: holderUID, Gid: holderGID, Groups: []uint32{}}, Pdeathsig: syscall.SIGKILL}, nil
}

func (v *vmRuntime) beforeOwnedStart(run *execRun, cmd *exec.Cmd) (func(), error) {
	if v.memory == nil {
		return nil, vmError("the VM memory boundary is unavailable")
	}
	if err := vmCommandCredentials(run.vmRole, v.plan.Args.VM.Docker, cmd.SysProcAttr); err != nil {
		return nil, err
	}
	groupID, err := v.newGroupID()
	if err != nil {
		return nil, err
	}
	group, err := v.memory.member(cmd.SysProcAttr, groupID, run.vmRole)
	if err != nil {
		return nil, err
	}
	if run.vmRole == vmDockerCommand {
		cmd.Args = append(cmd.Args, "--cgroup-parent=/agents/"+groupID+"/containers")
	}
	run.vmGroup = groupID
	// Bound Go's own copy goroutines if cleanup fails and a descendant
	// retains a pipe after the owned leader exits. No PID waiter is added.
	cmd.WaitDelay = time.Second
	return func() {
		group.Close()
		if cmd.Process == nil {
			if err := v.memory.releaseMember(groupID); err != nil {
				v.setError(err)
				go v.init.end()
			}
		}
	}, nil
}

func (v *vmRuntime) beforeOwnedReap(run *execRun) {
	if v.memory != nil && run.vmGroup != "" {
		if err := v.memory.killMember(run.vmGroup); err != nil {
			v.init.log.Error("cannot end owned guest cgroup", "err", err)
			v.setError(err)
		}
	}
}

func (v *vmRuntime) afterOwnedReap(run *execRun) {
	if v.memory != nil && run.vmGroup != "" {
		if err := v.memory.releaseMember(run.vmGroup); err != nil {
			v.setError(err)
			go v.init.end()
		}
	}
}

func (v *vmRuntime) capture(clean bool) (result error) {
	defer func() {
		if result != nil {
			v.setError(result)
		}
	}()
	v.mu.Lock()
	preparation := v.prepareDone
	v.mu.Unlock()
	if preparation != nil {
		<-preparation
	}
	v.waitFileOperations()
	v.mu.Lock()
	applied := v.applied && !v.failed && v.endErr == nil
	v.mu.Unlock()
	if !applied || v.state == nil {
		return nil
	}
	if err := v.state.capture(clean); err != nil {
		return err
	}
	return nil
}

func (v *vmRuntime) beginStop() {
	v.cancel()
	v.mu.Lock()
	preparation := v.prepareDone
	v.mu.Unlock()
	if preparation != nil {
		<-preparation
	}
	if err := v.stopDocker(); err != nil {
		v.setError(err)
	}
	v.mu.Lock()
	streams := make([]*link.Stream, 0, len(v.seedIO))
	for stream := range v.seedIO {
		streams = append(streams, stream)
	}
	holderIO := make(map[*link.Stream]net.Conn, len(v.holderIO))
	for stream, conn := range v.holderIO {
		holderIO[stream] = conn
	}
	v.mu.Unlock()
	for stream, conn := range holderIO {
		_ = conn.SetDeadline(time.Now())
		_ = stream.SetDeadline(time.Now())
		_ = conn.Close()
		_ = stream.Close()
	}
	for _, stream := range streams {
		_ = stream.SetDeadline(time.Now())
		_ = stream.Close()
	}
}

func (v *vmRuntime) setError(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.endErr == nil {
		v.endErr = err
	}
}

// newGroupID never reuses an identity during this VM, even after removing a
// completed leaf. Random session IDs alone have a birthday collision risk.
// The sole owned Start holds Init.mu across this counter and actual spawn.
func (v *vmRuntime) newGroupID() (string, error) {
	if v.nextGroup == ^uint64(0) {
		return "", vmError("the VM command identity space is exhausted")
	}
	v.nextGroup++
	value := v.nextGroup
	var encoded [13]byte
	for i := len(encoded) - 1; i >= 0; i-- {
		encoded[i] = api.SessionIDAlphabet[value&31]
		value >>= 5
	}
	return "g" + string(encoded[:]), nil
}

func (v *vmRuntime) controlOperation() (func(), error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ctx.Err() != nil {
		return nil, vmError("the VM is stopping before its control operation")
	}
	v.controlWG.Add(1)
	return v.controlWG.Done, nil
}

func (v *vmRuntime) waitFileOperations() {
	v.seedWG.Wait()
	v.holderWG.Wait()
	v.controlWG.Wait()
}

// A failed PID1 must not return/panic and let monitor status obscure the
// failure. The outside canonical owner has its normal bounded stop/kill path.
// This is an error state only; there is no retry or successful fallback.
func parkVMFailure() error {
	for {
		time.Sleep(time.Hour)
	}
}
