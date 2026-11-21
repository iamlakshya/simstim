//go:build linux

package microvm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

func slotAccountModeV1(facts api.CapProbe) bool {
	return slices.Contains(facts.RootSteps, "launcher") && slices.Contains(facts.RootSteps, "slots")
}

const slotUIDModeV1Name = "own-account"

func slotUIDModeV1(facts api.CapProbe) string {
	if slotAccountModeV1(facts) {
		return slotUIDModeV1Name
	}
	return uidMode(facts)
}
func savedUIDModeV1(facts api.CapProbe, mode string) bool {
	if mode == slotUIDModeV1Name {
		return slotAccountModeV1(facts)
	}
	return mode == uidMode(facts)
}
func slotPreparationModeV1(ctx context.Context, facts api.CapProbe) (string, error) {
	mode := slotUIDModeV1(facts)
	if mode != slotUIDModeV1Name {
		return mode, nil
	}
	slots, err := slotlaunch.SlotsNativeV1(ctx)
	if err != nil {
		return "", err
	}
	if len(slots) != slotlaunch.SlotCount {
		return "", fmt.Errorf("the installed slot producer has no complete native slot inventory")
	}
	for i, slot := range slots {
		if slot.Number != i {
			return "", fmt.Errorf("the installed slot producer returned an invalid native slot inventory")
		}
	}
	return mode, nil
}

func (s *sandbox) destroySlotDataV1(ctx context.Context) error {
	s.mu.Lock()
	command := s.command
	s.mu.Unlock()
	if actual, ok := command.(*slotVMProcess); ok && actual.source != nil {
		if used, err := actual.source.DestroyReservedDataV2(ctx); used || err != nil {
			return err
		}
	}
	return slotlaunch.DestroyNativeV1(ctx, s.spec.ID)
}

func (s *sandbox) slotCertificationFactsV1(ctx context.Context, facts api.CapProbe) (api.CapProbe, error) {
	s.mu.Lock()
	mode := s.uid
	control := s.control
	clean := s.cleanExitLocked()
	s.mu.Unlock()
	if mode != slotUIDModeV1Name {
		facts.RootSteps = slices.DeleteFunc(slices.Clone(facts.RootSteps), func(step string) bool { return step == "launcher" || step == "slots" })
		return facts, nil
	}
	if !slotAccountModeV1(facts) || control == nil {
		return facts, fmt.Errorf("the own-account certification has no original optional root source")
	}
	proof, ok := control.native.(*slotNativeProof)
	if !ok || proof == nil {
		return facts, fmt.Errorf("the own-account certification has no concrete native slot proof")
	}
	if clean {
		proof.mu.Lock()
		ready := proof.ready
		source := proof.source
		proof.mu.Unlock()
		if ready && source != nil {
			code, signal, known := source.ExitStatusV1()
			if known && code == 0 && signal == "" && source.PhysicalExitV1() {
				return facts, nil
			}
		}
	}
	expected := vmneed.NeedV1{BootV1: s.spec.VM.Clone(), Driver: s.driver.Name(), CPUs: s.spec.CPUs, DiskMax: s.spec.DiskMax}
	if err := proof.check(ctx, expected); err != nil {
		return facts, err
	}
	return facts, nil
}

// startSlotV1 is reached only after Start's unchanged fresh native kernel,
// memory, namespace, KVM, seccomp, Landlock and NoDump gates. The probe's
// optional root-step history selects this branch; it grants no native right.
func (s *sandbox) startSlotV1(ctx context.Context, facts api.CapProbe) (_ net.Conn, returned error) {
	files, err := s.driver.openBootFiles(ctx, s.spec)
	if adoptErr := s.adoptBootFilesV2(files); adoptErr != nil {
		return nil, s.startFailed(errors.Join(err, adoptErr))
	}
	defer s.finishBootFilesAtReturnV2(&returned)
	if err != nil {
		return nil, s.startFailed(err)
	}
	if err := s.driver.options.Reference(ctx, s.spec); err != nil {
		return nil, s.startFailed(err)
	}
	s.mu.Lock()
	s.agentsLease, files.agentsLease = files.agentsLease, nil
	s.uid = slotUIDModeV1Name
	s.mu.Unlock()
	execution, cancel := context.WithCancel(s.owner)
	interrupted := make(chan struct{})
	stopStartup := context.AfterFunc(ctx, func() { cancel(); close(interrupted) })
	stoppedStartup := false
	finishStartup := func() {
		if stoppedStartup {
			return
		}
		stoppedStartup = true
		if !stopStartup() {
			<-interrupted
		}
	}
	defer finishStartup()
	s.mu.Lock()
	s.cancel = cancel
	stopped := s.forced
	s.mu.Unlock()
	if stopped {
		cancel()
		return nil, s.startFailed(fmt.Errorf("the slot was stopped before its native start"))
	}
	boot, cancelBoot := context.WithTimeout(ctx, BootTimeout)
	defer cancelBoot()
	until, _ := boot.Deadline()
	connection, control, command, err := s.launchSlotRunner(execution, files, until)
	s.mu.Lock()
	s.command, s.control, s.connection = command, control, connection
	s.mu.Unlock()
	if command != nil {
		err = errors.Join(err, s.attachBootFileCommandV2(command))
	}
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
		return nil, err
	}
	checked, err := link.ReadGuestHello(boot, connection)
	if err != nil {
		s.Kill()
		s.Wait()
		return nil, err
	}
	finishStartup()
	if err := ctx.Err(); err != nil {
		s.Kill()
		s.Wait()
		return nil, err
	}
	owned := &ownedGuestLink{Conn: checked}
	s.mu.Lock()
	s.connection, s.helloChecked, s.bootFacts = owned, true, facts
	s.mu.Unlock()
	return owned, nil
}

// requestForSlot consumes the canonical engine's actual retained role vector.
// It never opens a supplied path or treats this byte projection as an OP.
func requestForSlot(spec wall.Spec, files *bootFiles) (slotlaunch.Request, error) {
	if err := validateVMSpec(spec); err != nil {
		return slotlaunch.Request{}, err
	}
	if files == nil || spec.VM == nil || spec.DiskMax%(1<<30) != 0 || spec.VM.GuestRAM%(1<<20) != 0 {
		return slotlaunch.Request{}, fmt.Errorf("the slot launch has no exact GiB disk and MiB guest-memory projection")
	}
	vector := []*os.File{files.vmm, files.kernel, files.tools, files.image, files.agents}
	digests := make([]string, 5)
	for i, file := range vector {
		if file == nil {
			return slotlaunch.Request{}, fmt.Errorf("an original slot runtime role is absent")
		}
		st, err := file.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 {
			return slotlaunch.Request{}, fmt.Errorf("an original slot runtime role is not a bounded regular file")
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, io.NewSectionReader(file, 0, st.Size())); err != nil {
			return slotlaunch.Request{}, err
		}
		digests[i] = hex.EncodeToString(hash.Sum(nil))
	}
	kind, err := backendForSpec(spec)
	if err != nil {
		return slotlaunch.Request{}, err
	}
	for i, role := range []vmneed.Role{{Kind: supply.KindVMM, Name: kind.vmmName()}, {Kind: supply.KindKernel, Name: kind.kernelName()}, {Kind: supply.KindTools, Name: "tools"}} {
		item, ok := spec.VM.Runtime.Find(role.Kind, role.Name, runtime.GOARCH)
		if !ok || item.SHA256 != digests[i] {
			return slotlaunch.Request{}, fmt.Errorf("the original slot runtime role differs from the complete Need")
		}
	}
	if digests[4] != spec.VM.Agents.SHA256 {
		return slotlaunch.Request{}, fmt.Errorf("the original slot agents disk differs from the complete Need")
	}
	tools, _ := spec.VM.Runtime.Find(supply.KindTools, "tools", runtime.GOARCH)
	return slotlaunch.Request{Op: "start", Session: spec.ID, Driver: kind.name(), VMM: digests[0], Kernel: digests[1], Tools: digests[2], Image: digests[3], Agents: digests[4], ToolsRootHash: tools.RootHash, ToolsDataBlocks: tools.DataBlocks, ImageRootHash: spec.VM.Image.RootHash, ImageDataBlocks: spec.VM.Image.DataBlocks, CPUs: spec.CPUs, MemoryMiB: spec.VM.GuestRAM / (1 << 20), DiskMaxGiB: spec.DiskMax / (1 << 30)}, nil
}

// launchSlotRunner is the distinct root-source branch of the same engine.
// The original supervisor OP/Need gate and runtime leases remain with Start.
func (s *sandbox) launchSlotRunner(ctx context.Context, files *bootFiles, until time.Time) (net.Conn, *runnerControl, ownedVMProcess, error) {
	request, err := requestForSlot(s.spec, files)
	if err != nil {
		return nil, nil, nil, err
	}
	source, err := s.startOriginalSlotSourceV1(ctx, request, files)
	if err != nil {
		if source != nil {
			return nil, nil, &slotVMProcess{source: source}, err
		}
		return nil, nil, nil, err
	}
	owner := &slotVMProcess{source: source}
	boot, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	var originalGuest net.Conn
	connection, err := connectGuest(boot, slotlaunch.RootPath()+"/vms/"+s.spec.ID+"/vsock.sock", func(ctx context.Context, _ string) (net.Conn, error) {
		actual, err := source.DialGuestV1(ctx)
		if err == nil {
			originalGuest = actual
		}
		return actual, err
	}, pauseVMConnect)
	if err != nil {
		_ = source.Signal(os.Kill)
		return nil, nil, owner, err
	}
	proof := &slotNativeProof{source: source, request: request, spec: cloneBindingSpec(s.spec), guest: originalGuest}
	ours, theirs := net.Pipe()
	server := &slotControlServer{connection: theirs, encoder: json.NewEncoder(theirs), source: source, proof: proof, driver: s.driver.Name()}
	owner.server = server
	control := startRunnerControl(ours, ours, nil, proof)
	go server.serve(ctx)
	return connection, control, owner, nil
}

type slotNativeProof struct {
	mu            sync.Mutex
	source        *slotlaunch.NativeSourceV1
	request       slotlaunch.Request
	spec          wall.Spec
	guest         net.Conn
	ready, closed bool
}

func (p *slotNativeProof) acceptReady(message runnerMessage, control *proto.UnixControlV1) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	kind, err := backendForSpec(p.spec)
	if err != nil {
		return err
	}
	if p.ready || p.closed || message.Session != p.spec.ID || message.Driver != kind.name() || len(control.Files) != 0 || control.Credentials != nil {
		return fmt.Errorf("the slot readiness message has no exact original native source")
	}
	if err := p.source.SameSourceV1(context.Background(), p.request); err != nil {
		return err
	}
	if err := p.source.SameGuestV1(context.Background(), p.guest); err != nil {
		return err
	}
	p.ready = true
	return nil
}

func (p *slotNativeProof) check(ctx context.Context, expected vmneed.NeedV1) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready || p.closed || p.source == nil {
		return fmt.Errorf("the slot has no live original native proof")
	}
	kind, err := backendForSpec(p.spec)
	if err != nil {
		return err
	}
	actual := vmneed.NeedV1{BootV1: p.spec.VM.Clone(), Driver: kind.name(), CPUs: p.spec.CPUs, DiskMax: p.spec.DiskMax}
	left, err := vmneed.EncodeV1(actual, runtime.GOARCH)
	if err != nil {
		return err
	}
	right, err := vmneed.EncodeV1(expected, runtime.GOARCH)
	if err != nil || !bytes.Equal(left, right) {
		return errors.Join(err, fmt.Errorf("the slot's complete original Need changed"))
	}
	if err := p.source.SameSourceV1(ctx, p.request); err != nil {
		return err
	}
	return p.source.SameGuestV1(ctx, p.guest)
}

func (p *slotNativeProof) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.source != nil && p.source.PhysicalExitV1() {
		return p.source.Close()
	}
	return nil
}

type slotSandboxBinding struct {
	sandbox    *sandbox
	connection *ownedGuestLink
	proof      *slotNativeProof
}

func (b *slotSandboxBinding) sessionID() string { return b.proof.spec.ID }
func (b *slotSandboxBinding) checkNativeBinding(ctx context.Context, connection net.Conn, expected vmneed.NeedV1) error {
	if connection != b.connection {
		return fmt.Errorf("the slot continuation has another Start-returned stream")
	}
	s := b.sandbox
	s.mu.Lock()
	live := s.started && s.helloChecked && !s.forced && !s.destroyed && !s.destroying && !s.preparedReleased && !s.physicalExit && s.connection == b.connection && s.control != nil && s.control.native == b.proof
	owner := s.owner
	s.mu.Unlock()
	if !live || owner == nil || owner.Err() != nil {
		return fmt.Errorf("the original slot engine owner ended")
	}
	return b.proof.check(ctx, expected)
}

func newSlotSandboxBindingV1(ctx context.Context, s *sandbox, connection *ownedGuestLink, proof *slotNativeProof) (*NativeBindingV1, error) {
	expected := vmneed.NeedV1{BootV1: s.spec.VM.Clone(), Driver: s.driver.Name(), CPUs: s.spec.CPUs, DiskMax: s.spec.DiskMax}
	binding := &slotSandboxBinding{sandbox: s, connection: connection, proof: proof}
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return nil, err
	}
	return &NativeBindingV1{verifier: binding}, nil
}

type slotVMProcess struct {
	source *slotlaunch.NativeSourceV1
	server *slotControlServer
}

func (p *slotVMProcess) Signal(signal os.Signal) error { return p.source.Signal(signal) }
func (p *slotVMProcess) PhysicalExitV1() bool {
	return p != nil && p.source != nil && p.source.PhysicalExitV1()
}

func (p *slotVMProcess) ReleaseOriginalEvidenceV1() error {
	if p == nil || p.source == nil {
		return nil
	}
	return p.source.ReleaseClosedEvidenceV1()
}
func (p *slotVMProcess) Wait() error {
	err := p.source.Wait()
	if p.server != nil {
		code, signal, known := p.source.ExitStatusV1()
		exit := &runnerExit{Code: code, Signal: signal}
		if !known {
			exit.Code = -1
			exit.Error = "the slot ended without its owned VMM exit status"
		}
		_ = p.server.send(runnerMessage{Kind: "exit", Exit: exit})
		p.server.connection.Close()
	}
	if p.source.PhysicalExitV1() {
		err = errors.Join(err, p.source.Close())
	}
	return err
}
