//go:build linux

package microvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/supply"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker/backend"
	"golang.org/x/sys/unix"
)

// localLaunchOriginV1 is a restriction from the REAL normal launch creator.
// Its disk/runtime/input/Cmd objects are borrowed from that same factory and
// remain with their original owners. No plan decoder, saved Spec, reply flag,
// PID or descriptor vector can construct an origin or outside Source.
type localLaunchOriginV1 struct {
	mu              sync.Mutex
	sandbox         *sandbox
	command         *jail.Cmd
	controller      *runnerControl
	files           *bootFiles
	disk            *os.File
	diskBirth       os.FileInfo
	input           io.WriteCloser
	plan            runnerPlan
	encoded         []byte
	sent            bool
	complete        bool
	bound           bool
	pidfd, endpoint *os.File
	roles           map[*os.File]os.FileInfo
	config          *os.File
	configBirth     os.FileInfo
	owned           map[*os.File]bool
}

// Called ONLY by actual launchRunnerWithDiskV1 after its original native
// Cmd/StdinPipe/control exist. The supplier excludes its opaque RO branch:
// that branch's initial anchor is never a raw disk or this normal receipt.
func (s *sandbox) captureLocalLaunchOriginV1(ctx context.Context, plan runnerPlan, files *bootFiles, disk *os.File, command *jail.Cmd, input io.WriteCloser, controller *runnerControl, configuration ...*protectedBootConfig) (*localLaunchOriginV1, error) {
	if ctx == nil || files == nil || disk == nil || command == nil || input == nil || controller == nil || plan.Version != 1 {
		return nil, fmt.Errorf("the normal launch origin has no actual creator objects")
	}
	proof, ok := controller.native.(*receivedVMMProof)
	if !ok || proof == nil {
		return nil, fmt.Errorf("the normal launch origin has another native readiness receiver")
	}
	immutable := plan
	immutable.Spec = cloneBindingSpec(plan.Spec)
	encoded, err := json.Marshal(immutable)
	if err != nil {
		return nil, err
	}
	// Decode the exact wire bytes into a private deep copy as well. The
	// creator writes this SAME immutable byte vector, never encodes plan
	// again after a caller could change nested runtime/image vectors.
	var retained runnerPlan
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&retained); err != nil {
		return nil, err
	}
	info, err := disk.Stat()
	if err != nil {
		return nil, err
	}
	origin := &localLaunchOriginV1{sandbox: s, command: command, controller: controller, files: files, disk: disk, diskBirth: info, input: input, plan: retained, encoded: append(append([]byte(nil), encoded...), '\n'), roles: make(map[*os.File]os.FileInfo), owned: make(map[*os.File]bool)}
	s.mu.Lock()
	s.partialSlotOwners = append(s.partialSlotOwners, origin)
	s.mu.Unlock()
	for _, file := range []*os.File{files.vmm, files.kernel, files.tools, files.image, files.agents, files.helper} {
		if file == nil {
			return origin, fmt.Errorf("the actual normal launch has an absent runtime role")
		}
		// The actual boot custody keeps these SAME original objects while
		// this native command is live. The origin borrows, never reopens.
		stat, err := file.Stat()
		if err != nil {
			return origin, err
		}
		origin.roles[file] = stat
	}
	if kind, err := backendForSpec(retained.Spec); err != nil {
		return origin, err
	} else if kind == firecrackerBackend {
		if len(configuration) != 1 || configuration[0] == nil {
			return origin, fmt.Errorf("the actual Firecracker origin has no original protected configuration")
		}
		if err := configuration[0].check(); err != nil {
			return origin, err
		}
		fd, err := unix.FcntlInt(configuration[0].file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return origin, err
		}
		origin.config = os.NewFile(uintptr(fd), "original-normal-vmm-configuration")
		origin.owned[origin.config] = false
		origin.configBirth, err = origin.config.Stat()
		if err != nil {
			return origin, err
		}
	}
	if err := origin.checkInputsV1(ctx); err != nil {
		return origin, err
	}
	proof.mu.Lock()
	defer proof.mu.Unlock()
	if proof.ready || proof.closed || proof.origin != nil || proof.session != retained.Spec.ID {
		return nil, fmt.Errorf("the real normal launch already has another native creator")
	}
	proof.origin = origin
	return origin, nil
}

func (origin *localLaunchOriginV1) sendPlanV1() error {
	if origin == nil {
		return fmt.Errorf("the normal launch has no original immutable plan")
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	if origin.sent || origin.input == nil {
		return fmt.Errorf("the actual normal launch plan was already consumed")
	}
	origin.sent = true // A partial write may already have reached this child.
	if n, err := origin.input.Write(origin.encoded); err != nil || n != len(origin.encoded) {
		return errors.Join(io.ErrShortWrite, err)
	}
	origin.complete = true
	return nil
}

func (origin *localLaunchOriginV1) checkInputsV1(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if origin.plan.Spec.VM == nil || origin.disk == nil || origin.files == nil || origin.diskBirth == nil {
		return fmt.Errorf("the real normal launch input owner ended")
	}
	current, err := origin.disk.Stat()
	if err != nil || !os.SameFile(origin.diskBirth, current) || !current.Mode().IsRegular() || current.Sys().(*syscall.Stat_t).Nlink != 1 || current.Size() != origin.plan.Spec.DiskMax || current.Mode().Perm() != 0o600 {
		return fmt.Errorf("the normal launch moved or changed its actual original disk")
	}
	for file, before := range origin.roles {
		after, err := file.Stat()
		if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return fmt.Errorf("the actual normal launch changed an original runtime/FD role")
		}
	}
	kind, err := backendForSpec(origin.plan.Spec)
	if err != nil {
		return err
	}
	for index, kindName := range []string{supply.KindVMM, supply.KindKernel} {
		name := kind.vmmName()
		file := origin.files.vmm
		if index == 1 {
			name = "guest"
			file = origin.files.kernel
		}
		item, ok := origin.plan.Spec.VM.Runtime.Find(kindName, name, origin.plan.Spec.VM.Image.Arch)
		if !ok || file == nil {
			return fmt.Errorf("the normal launch lost an original verified runtime role")
		}
		if index == 0 {
			err = supply.CheckSealedRuntime(item, file)
		} else {
			err = supply.CheckRuntimeDescriptor(ctx, item, file)
		}
		if err != nil {
			return err
		}
	}
	return origin.checkSyncTemplateV1()
}

func (origin *localLaunchOriginV1) Close() error {
	if origin == nil {
		return nil
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	var err error
	for file, retired := range origin.owned {
		if retired {
			continue
		}
		known, closeErr := origin.sandbox.closeOwnedDescriptorV2(file)
		origin.owned[file] = known
		if !known {
			err = errors.Join(err, closeErr)
		}
	}
	return err
}

func (origin *localLaunchOriginV1) checkSyncTemplateV1() error {
	spec := origin.plan.Spec
	kind, err := backendForSpec(spec)
	if err != nil {
		return err
	}
	api, vsock := vmSocketPaths(vmDataDir(spec))
	if kind == firecrackerBackend {
		// The closed actual helper consumes exactly this config creator,
		// whose four drives set io_engine=Sync. It accepts no argv/config
		// override from the guest or a subsequent current-copy request.
		_, configuration, _, err := fc.Command(spec, fc.Paths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", Config: filepath.Join(spec.SessionDir, firecrackerConfigName), API: api, Vsock: vsock})
		if err != nil {
			return err
		}
		if origin.config == nil || origin.configBirth == nil {
			return fmt.Errorf("the actual Firecracker creator lost its original config descriptor")
		}
		actual, readErr := io.ReadAll(io.NewSectionReader(origin.config, 0, (64<<10)+1))
		info, statErr := origin.config.Stat()
		if readErr != nil || statErr != nil || !os.SameFile(origin.configBirth, info) || !bytes.Equal(actual, configuration) {
			return errors.Join(fmt.Errorf("the actual Firecracker config differs from its immutable real launch"), readErr, statErr)
		}
		var config struct {
			Drives []struct {
				IOEngine string `json:"io_engine"`
			} `json:"drives"`
		}
		if json.Unmarshal(configuration, &config) != nil || len(config.Drives) != 4 {
			return fmt.Errorf("the actual Firecracker creator has no fixed four-drive configuration")
		}
		for _, drive := range config.Drives {
			if drive.IOEngine != "Sync" {
				return fmt.Errorf("the actual Firecracker creator permits an asynchronous disk engine")
			}
		}
		return nil
	}
	args, _, err := bootCommand(bootSpec{Runtime: spec.VM.Runtime, Image: spec.VM.Image, Paths: bootPaths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", API: api, Vsock: vsock}, GuestRAM: spec.VM.GuestRAM, CPUs: spec.CPUs, SessionDirect: origin.plan.SessionDirect})
	if err != nil {
		return err
	}
	return checkLocalSyncDisksV1(args)
}

func checkLocalSyncDisksV1(args []string) error {
	count := 0
	for _, arg := range args {
		if !strings.HasPrefix(arg, "path=/proc/self/fd/") || !strings.Contains(arg, ",image_type=raw,") {
			continue
		}
		count++
		if !strings.Contains(arg, ",_disable_io_uring=on,") || !strings.Contains(arg, ",_disable_aio=on,") {
			return fmt.Errorf("the real local VMM creator permits an asynchronous raw disk engine")
		}
	}
	if count != 4 {
		return fmt.Errorf("the real local VMM creator has another fixed raw disk role count")
	}
	return nil
}

func (origin *localLaunchOriginV1) bindReadyV1(pidfd, endpoint *os.File) error {
	origin.mu.Lock()
	defer origin.mu.Unlock()
	if !origin.complete || origin.bound || origin.command == nil || origin.controller == nil || origin.command.PhysicalExitV1() {
		return fmt.Errorf("the original normal creator did not send its exact complete immutable plan")
	}
	if err := errors.Join(origin.checkInputsV1(context.Background()), checkNativeVMMPair(pidfd, endpoint)); err != nil {
		return err
	}
	origin.pidfd, origin.endpoint, origin.bound = pidfd, endpoint, true
	return nil
}

func (binding *sandboxNativeBinding) checkLocalLaunchOriginV1(ctx context.Context) error {
	binding.proof.mu.Lock()
	origin := binding.proof.origin
	pidfd, endpoint := binding.proof.pidfd, binding.proof.socket
	binding.proof.mu.Unlock()
	if origin == nil {
		return fmt.Errorf("the original local runtime has no real synchronous launch-creator receipt")
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	binding.sandbox.mu.Lock()
	command, controller, diskOwner := binding.sandbox.command, binding.sandbox.control, binding.sandbox.snapshotDisk
	binding.sandbox.mu.Unlock()
	if !origin.complete || !origin.bound || origin.sandbox != binding.sandbox || origin.command != command || origin.controller != controller || origin.pidfd != pidfd || origin.endpoint != endpoint || diskOwner == nil || origin.command.PhysicalExitV1() {
		return fmt.Errorf("the real local launch origin moved to another actor or native binding")
	}
	return diskOwner.borrow(ctx, func(_ *os.Root, _ *os.Root, disk *os.File) error {
		if disk != origin.disk {
			return fmt.Errorf("the real local launch origin has another received raw disk object")
		}
		return errors.Join(origin.checkInputsV1(ctx), checkNativeVMMPair(pidfd, endpoint))
	})
}
