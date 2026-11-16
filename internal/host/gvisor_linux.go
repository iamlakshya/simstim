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
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
	"golang.org/x/sys/unix"
)

// NewGvisorDriver joins the original accepted native boot receipt, its signed
// runtime closure and its retained host storage domain to the actual jail.
// It takes no caller-selected owners, callbacks, namespace policy or runtime.
func NewGvisorDriver() (wall.Driver, error) {
	driver, err := gvisor.New(gvisor.Options{ExpectedRuntime: ExpectedOriginalRuntime,
		Domain: OriginalBootDomain, Command: commandGvisorOriginalV1})
	if err != nil {
		return nil, err
	}
	return &originalGvisorDriverV1{driver: driver}, nil
}

type originalGvisorDriverV1 struct{ driver *gvisor.Driver }

func (*originalGvisorDriverV1) Name() string      { return "gvisor" }
func (*originalGvisorDriverV1) Level() wall.Level { return wall.GVisor }
func (d *originalGvisorDriverV1) Probe(ctx context.Context) wall.Probe {
	return d.driver.Probe(ctx)
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
	receipt, err := checkOriginalGvisorSpecV1(ctx, spec)
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
	manifest, err := ExpectedOriginalRuntime(ctx)
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
	binds := slices.Clone(request.Spec.Binds)
	for index, executable := range request.Executables {
		item := items[index]
		if executable.Target != gvisor.RuntimeTargetV1(item.Name) {
			return nil, vmNeedError("gvisor changed an original signed executable role")
		}
		if err := supply.CheckSealedRuntime(item, executable.File); err != nil {
			return nil, err
		}
		binds = append(binds, jail.Bind{Path: executable.Target, File: executable.File})
	}
	for index, mount := range request.Mounts {
		file := request.MountFiles[index]
		if file == nil || mount.Identity == "" {
			return nil, vmNeedError("gvisor lost a captured filesystem capability")
		}
		identity, err := files.Identity(file)
		if err != nil || identity != mount.Identity {
			return nil, errors.Join(vmNeedError("gvisor substituted a captured filesystem object"), err)
		}
		binds = append(binds, jail.Bind{Path: mount.Host, File: file, Write: mount.Write})
	}
	if err := receipt.boot.operation.withCurrentRead(ctx, func(context.Context) error {
		return checkGvisorControlFilesV1(receipt, spec, request)
	}); err != nil {
		return nil, err
	}
	binds, err = coalesceGvisorBindsV1(binds)
	if err != nil {
		return nil, err
	}
	commandSpec := request.Spec
	commandSpec.Binds = binds
	owned, cancel := context.WithCancel(ctx)
	command, err := jail.CommandGvisorV1(owned, commandSpec)
	if err != nil {
		cancel()
		return nil, err
	}
	command.Stdout, command.Stderr = request.Log, request.Log
	return &originalGvisorCommandV1{Cmd: command, ctx: ctx, receipt: receipt, spec: spec, cancel: cancel}, nil
}

type originalGvisorCommandV1 struct {
	*jail.Cmd
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
	return command.Cmd.Start()
}

func (command *originalGvisorCommandV1) Wait() error {
	err := command.Cmd.Wait()
	if command.PhysicalExitV1() {
		command.cancel()
	}
	return err
}

func checkGvisorControlFilesV1(receipt *OriginalVMNeed, spec wall.Spec, request gvisor.CommandRequest) error {
	root := receipt.boot.operation.native.root
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
