//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
	"golang.org/x/sys/unix"
)

// NewGvisorDriver joins the original accepted native boot receipt, its signed
// runtime closure and its retained host storage domain to the actual jail.
// It takes no caller-selected owners, callbacks, namespace policy or runtime.
func NewGvisorDriver() (wall.Driver, error) {
	driver, err := gvisor.New(gvisor.Options{ExpectedRuntime: gvisorExpectedRuntimeV1,
		Domain: gvisorNativeDomainV1, Command: commandGvisorOriginalV1})
	if err != nil {
		return nil, err
	}
	return &originalGvisorDriverV1{driver: driver}, nil
}

type originalGvisorDriverV1 struct{ driver *gvisor.Driver }

func (*originalGvisorDriverV1) Name() string      { return "gvisor" }
func (*originalGvisorDriverV1) Level() wall.Level { return wall.GVisor }
func (d *originalGvisorDriverV1) Probe(ctx context.Context) wall.Probe {
	purpose := gvisorProbeFromContextV1(ctx)
	if purpose == nil {
		return gvisor.ConstructionProbeV1(ctx)
	}
	if err := purpose.beginResourceV1(ctx); err != nil {
		return wall.Probe{Driver: "gvisor", Level: wall.GVisor, Reason: err.Error(), Fix: "Retain the original probe owner and retry its actual cleanup."}
	}
	defer purpose.probeWG.Done()
	probe, resource := d.driver.ProbeOwnedV1(ctx)
	purpose.mu.Lock()
	if resource != nil {
		purpose.runtime = resource
	}
	purpose.mu.Unlock()
	if probe.OK {
		if err := purpose.check(ctx); err != nil {
			probe.OK, probe.Reason, probe.Fix = false, err.Error(), "Retain the same native account and selected runtime before probing again."
		} else {
			key, err := selectedRuntimeKey(purpose.selected.manifest)
			if err != nil {
				probe.OK, probe.Reason, probe.Fix = false, err.Error(), "Prepare the original signed runtime again."
			} else {
				if probe.Facts == nil {
					probe.Facts = map[string]string{}
				}
				probe.Facts["runtime_manifest"] = key
				probe.Facts["native_closure_v1"] = purpose.physicalKey
			}
		}
	}
	return probe
}
func (d *originalGvisorDriverV1) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	if _, err := checkOriginalGvisorSpecV1(ctx, spec); err != nil {
		return nil, err
	}
	return d.driver.Prepare(ctx, spec)
}
func (d *originalGvisorDriverV1) Open(ctx context.Context, dir string) (wall.Sandbox, error) {
	spec, ok := wall.ExpectedSpec(ctx)
	if !ok || spec.SessionDir != dir {
		return nil, vmNeedError("the gvisor layout has no original protected plan")
	}
	if _, err := checkOriginalGvisorSpecV1(ctx, spec); err != nil {
		return nil, err
	}
	return d.driver.Open(ctx, dir)
}

func checkOriginalGvisorSpecV1(ctx context.Context, spec wall.Spec) (*OriginalVMNeed, error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil || receipt.need != nil || len(receipt.rawNeed) != 0 || receipt.boot.operation == nil {
		return nil, vmNeedError("gvisor has no original accepted process-runtime boot receipt")
	}
	manifest, err := receipt.boot.operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if manifest.Wall == nil || manifest.Wall.Level != "gvisor" || manifest.Wall.Driver != "gvisor" ||
		manifest.ID != spec.ID || spec.ID != receipt.boot.loan.Primary || manifest.RuntimeManifest == nil || len(manifest.VMNeedV1) != 0 ||
		spec.RuntimeManifest == nil || spec.Image != nil || spec.VM != nil || spec.Root != wall.Reset ||
		spec.MemoryMax != manifest.Limits.MemoryMax || spec.MemoryMax != receipt.spec.MemoryMax {
		return nil, vmNeedError("the gvisor plan differs from its original protected primary")
	}
	selected, err := ExpectedOriginalRuntime(ctx)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(selected, *manifest.RuntimeManifest) || !reflect.DeepEqual(selected, *spec.RuntimeManifest) {
		return nil, vmNeedError("the gvisor executables differ from the original signed runtime")
	}
	if _, err := gvisor.RuntimeItemsV1(selected, runtime.GOARCH); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(paths.SessionDir(spec.ID))
	if err != nil || spec.SessionDir != canonical {
		return nil, errors.Join(vmNeedError("gvisor lost its original protected session root"), err)
	}
	helper, err := os.Executable()
	if err != nil || filepath.Dir(helper) != spec.Helper {
		return nil, errors.Join(vmNeedError("gvisor selected another host helper"), err)
	}
	plan, err := SandboxStatePlan(manifest)
	if err != nil || !slices.Equal(plan.Dirs, spec.StateDirs) || !slices.Equal(plan.Files, spec.StateFiles) {
		return nil, errors.Join(vmNeedError("gvisor changed its original kept-state plan"), err)
	}
	if len(plan.Files) != 0 {
		if err := files.ValidateStateMounts(spec.SessionDir, spec.StateRestore, spec.StateCapture); err != nil {
			return nil, err
		}
	} else if spec.StateRestore != "" || spec.StateCapture != "" {
		return nil, vmNeedError("gvisor added state generations outside its protected file plan")
	}
	want := []wall.Mount{{Host: filepath.Join(canonical, "work"), Guest: "/work", Write: true}}
	if manifest.Files != nil {
		want = append(want, manifest.Files.Installs...)
		want = append(want, manifest.Files.Mounts...)
	} else {
		for _, entry := range manifest.Env {
			value, ok := strings.CutPrefix(entry, "PATH=")
			if !ok {
				continue
			}
			for _, folder := range filepath.SplitList(value) {
				if !filepath.IsAbs(folder) {
					continue
				}
				covered := false
				for _, prefix := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
					covered = covered || folder == prefix || strings.HasPrefix(folder, prefix+"/")
				}
				if !covered {
					want = append(want, wall.Mount{Host: folder, Guest: folder})
				}
			}
			break
		}
	}
	if !slices.Equal(want, spec.Mounts) {
		return nil, vmNeedError("gvisor changed its original granted filesystem plan")
	}
	var diskMax int64
	if manifest.Policy != nil && manifest.Policy.Sandbox.DiskMax != nil {
		diskMax, err = api.ParseSize(*manifest.Policy.Sandbox.DiskMax)
		if err != nil || diskMax < 0 {
			return nil, errors.Join(vmNeedError("the protected gvisor disk limit is invalid"), err)
		}
	}
	if spec.DiskMax != diskMax {
		return nil, vmNeedError("gvisor changed its original disk limit")
	}
	if _, err := OriginalBootDomain(ctx); err != nil {
		return nil, err
	}
	return receipt, nil
}

func commandGvisorOriginalV1(ctx context.Context, request gvisor.CommandRequest) (gvisor.OwnedCommand, error) {
	spec, ok := wall.ExpectedSpec(ctx)
	if !ok {
		return nil, vmNeedError("gvisor has no current original filesystem projection")
	}
	var receipt *OriginalVMNeed
	var err error
	purpose := gvisorProbeFromContextV1(ctx)
	if purpose != nil {
		err = purpose.check(ctx)
		if err == nil && !reflect.DeepEqual(spec, purpose.spec) {
			err = newRuntimeError("the physical probe changed its native purpose plan")
		}
	} else {
		receipt, err = checkOriginalGvisorSpecV1(ctx, spec)
	}
	if err != nil {
		return nil, err
	}
	if !slices.Equal(request.Spec.Argv, gvisor.CommandArgumentsV1(spec.SessionDir, spec.ID)) ||
		!slices.Equal(request.Spec.Env, gvisor.CommandEnvironmentV1()) || request.Spec.Subuid ||
		request.Spec.Net != jail.NetNone || request.Spec.Seccomp != "" || len(request.Spec.Rules) != 0 ||
		len(request.Spec.FileRulesV1) != 0 || len(request.Spec.Files) != 1 || request.Spec.Files[0] == nil ||
		len(request.Mounts) != len(request.MountFiles) || len(request.Spec.Binds) != 2 || request.Log == nil {
		return nil, vmNeedError("the gvisor command escaped its closed native boot plan")
	}
	if err := request.Topology.CheckSetupV1(spec, request.Mounts); err != nil {
		return nil, err
	}
	if request.ForkMounts == nil {
		return nil, vmNeedError("the gvisor command has no original fresh-home mount producer")
	}
	manifest, err := gvisorExpectedRuntimeV1(ctx)
	if err != nil {
		return nil, err
	}
	items, err := gvisor.RuntimeItemsV1(manifest, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if len(items) != len(request.Executables) {
		return nil, vmNeedError("gvisor lost part of its original signed companion closure")
	}
	if err := request.Suite.CheckOriginalV1(ctx, manifest, request.Executables); err != nil {
		return nil, errors.Join(vmNeedError("gvisor lost its original protected native code suite"), err)
	}
	binds := slices.Clone(request.Spec.Binds)
	for index, executable := range request.Executables {
		item := items[index]
		if executable.Target != gvisor.RuntimeTargetV1(item.Name) {
			return nil, vmNeedError("gvisor changed an original signed executable role")
		}
		binds = append(binds, jail.Bind{Path: executable.Target, File: executable.File})
	}
	mountBinds := make([]jail.Bind, 0, len(request.Mounts))
	for index, mount := range request.Mounts {
		file := request.MountFiles[index]
		if file == nil || mount.Identity == "" {
			return nil, vmNeedError("gvisor lost a captured filesystem capability")
		}
		identity, err := files.Identity(file)
		if err != nil || identity != mount.Identity {
			return nil, errors.Join(vmNeedError("gvisor substituted a captured filesystem object"), err)
		}
		mountBinds = append(mountBinds, jail.Bind{Path: mount.Host, File: file, Write: mount.Write})
	}
	var forkBinds []jail.Bind
	var routes map[string]string
	if purpose != nil {
		forkBinds, routes, err = request.ForkMounts.CommandBindsV1(ctx, purpose.root, spec, request.Mounts, request.MountFiles)
	} else {
		err = receipt.boot.operation.withCurrentRead(ctx, func(context.Context) error {
			var failure error
			forkBinds, routes, failure = request.ForkMounts.CommandBindsV1(ctx, receipt.boot.operation.native.root, spec, request.Mounts, request.MountFiles)
			return failure
		})
	}
	if err != nil {
		return nil, err
	}
	for index, mount := range request.Mounts {
		if routes[mount.Guest] == "" {
			binds = append(binds, mountBinds[index])
		}
	}
	binds = append(binds, forkBinds...)
	if purpose != nil {
		if err := checkGvisorControlFilesV1(purpose.root, spec, request); err != nil {
			return nil, err
		}
		if err := purpose.check(ctx); err != nil {
			return nil, err
		}
	} else {
		if err := receipt.boot.operation.withCurrentRead(ctx, func(context.Context) error {
			return checkGvisorControlFilesV1(receipt.boot.operation.native.root, spec, request)
		}); err != nil {
			return nil, err
		}
	}
	binds, err = coalesceGvisorBindsV1(binds)
	if err != nil {
		return nil, err
	}
	commandSpec := request.Spec
	commandSpec.Binds = binds
	owned, cancel := context.WithCancel(ctx)
	var source *jail.GvisorBootstrapV1
	if purpose != nil {
		source = purpose.bootstrap
	} else {
		source, err = jail.CaptureGvisorBootstrapV1(ctx)
		if err == nil {
			defer source.Close()
			var domain cachefs.Domain
			domain, err = OriginalBootDomain(ctx)
			if err == nil {
				err = requireCachedGvisorClosureV1(ctx, manifest, domain, source)
			}
		}
	}
	if err != nil {
		cancel()
		return nil, err
	}
	command, err := constructOriginalGvisorControllerV1(owned, request, spec, commandSpec, source)
	if err != nil {
		cancel()
		// A successful constructor followed by an input-close error still
		// owns an unstarted native command. Retire it on the cancelled early
		// path instead of dropping its captured code and private guest copy.
		if command != nil {
			err = errors.Join(err, command.ReleasePreparedV1())
			return command, err
		}
		return nil, err
	}
	command.Stdout, command.Stderr = request.Log, request.Log
	if purpose != nil {
		probe := &probeGvisorCommandV1{Cmd: command, suite: request.Suite, purpose: purpose, ctx: ctx, cancel: cancel}
		purpose.mu.Lock()
		if purpose.closed || purpose.command != nil {
			purpose.mu.Unlock()
			cancel()
			return command, errors.Join(newRuntimeError("the physical probe already owns a native command"), command.ReleasePreparedV1())
		}
		purpose.command = probe
		purpose.mu.Unlock()
		return probe, nil
	}
	return &originalGvisorCommandV1{Cmd: command, suite: request.Suite, ctx: ctx, receipt: receipt, spec: spec, cancel: cancel}, nil
}

type originalGvisorCommandV1 struct {
	*jail.Cmd
	suite   *gvisor.ProtectedSuiteV1
	ctx     context.Context
	receipt *OriginalVMNeed
	spec    wall.Spec
	cancel  context.CancelFunc
}

func (command *originalGvisorCommandV1) Start() error {
	receipt, err := checkOriginalGvisorSpecV1(command.ctx, command.spec)
	if err == nil && receipt != command.receipt {
		err = vmNeedError("the original gvisor boot receipt changed before native start")
	}
	if err == nil {
		manifest, expectedErr := gvisorExpectedRuntimeV1(command.ctx)
		err = expectedErr
		if err == nil {
			err = command.suite.CheckOriginalV1(command.ctx, manifest, nil)
		}
	}
	if err == nil {
		owner := receipt.boot.owner
		owner.mu.Lock()
		if owner.startup != receipt.boot || receipt.boot.ready || receipt.boot.gvisorIssued || !receipt.boot.operation.Valid(receipt.boot.loan.Primary) {
			err = vmNeedError("the original gvisor boot was consumed or ended before native start")
		} else {
			receipt.boot.gvisorIssued = true
		}
		owner.mu.Unlock()
	}
	if err != nil {
		command.cancel()
		// Start on the cancelled owner closes its captured jail pins without
		// launching a child. The actual Cmd still owns physical closure.
		return errors.Join(err, command.Cmd.Start())
	}
	return startOriginalGvisorControllerV1(command)
}

func (command *originalGvisorCommandV1) Wait() error {
	err := command.Cmd.Wait()
	if command.PhysicalExitV1() {
		command.cancel()
	}
	return err
}

func checkGvisorControlFilesV1(root *os.Root, spec wall.Spec, request gvisor.CommandRequest) error {
	for index, name := range []string{"bundle", "runsc"} {
		bound := request.Spec.Binds[index]
		if bound.Path != filepath.Join(spec.SessionDir, "gvisor", name) || bound.Write != (name == "runsc") || bound.File == nil {
			return vmNeedError("gvisor changed its original runtime control mount")
		}
		want, err := root.Lstat(filepath.Join("gvisor", name))
		if err != nil || !want.IsDir() || want.Mode()&os.ModeSymlink != 0 {
			return errors.Join(vmNeedError("gvisor lost its original runtime control directory"), err)
		}
		got, err := bound.File.Stat()
		if err != nil || !os.SameFile(want, got) {
			return errors.Join(vmNeedError("gvisor substituted its runtime control directory"), err)
		}
	}
	want, err := root.Lstat(filepath.Join("gvisor", "runsc.log"))
	if err != nil || !want.Mode().IsRegular() || want.Mode().Perm() != 0o600 {
		return errors.Join(vmNeedError("gvisor lost its actual private runtime log"), err)
	}
	got, err := request.Log.Stat()
	if err != nil || !os.SameFile(want, got) {
		return errors.Join(vmNeedError("gvisor selected another runtime log descriptor"), err)
	}
	flags, err := unix.FcntlInt(request.Log.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_APPEND == 0 || flags&unix.O_ACCMODE != unix.O_WRONLY {
		return errors.Join(vmNeedError("gvisor lost its append-only diagnostic descriptor"), err)
	}
	file := request.Spec.Files[0]
	var socket unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &socket); err != nil || socket.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return errors.Join(vmNeedError("gvisor lost its original descriptor-only guest link"), err)
	}
	address, err := unix.Getsockname(int(file.Fd()))
	if _, ok := address.(*unix.SockaddrUnix); err != nil || !ok {
		return errors.Join(vmNeedError("gvisor requires its Unix guest link"), err)
	}
	return nil
}

func coalesceGvisorBindsV1(in []jail.Bind) ([]jail.Bind, error) {
	var result []jail.Bind
	for _, bound := range in {
		if bound.File == nil {
			return nil, vmNeedError("gvisor cannot reopen a raw path as mount authority")
		}
		got, err := bound.File.Stat()
		if err != nil {
			return nil, err
		}
		duplicate := false
		for _, old := range result {
			if old.Path != bound.Path {
				continue
			}
			want, err := old.File.Stat()
			if err != nil || !os.SameFile(want, got) || old.Write != bound.Write {
				return nil, errors.Join(vmNeedError("gvisor has conflicting capabilities at one mount target"), err)
			}
			duplicate = true
			break
		}
		if !duplicate {
			result = append(result, bound)
		}
	}
	return result, nil
}

var _ wall.Driver = (*originalGvisorDriverV1)(nil)
