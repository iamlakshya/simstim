//go:build linux

// Package gvisor runs the pinned userspace kernel over the process wall's
// filesystem plan. Namespace and executable authority belong to the host's
// protected operation; a saved bundle or runtime inventory cannot issue them.
package gvisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

// Runtime is an owned, non-serialisable execution capability. Its fields are
// private: neither an installed-runtime list nor a caller's item can mint one.
// The canonical signed-runtime opener owns construction and checks all runsc
// companions from the same original protected operation before returning it.
type Runtime struct {
	projections     []*ControllerCodeV1
	store           *runtimecache.Store
	pendingFiles    []*os.File
	closeErr        error
	closing, closed bool
	mu              sync.Mutex
	protectedSuite  *ProtectedSuiteV1
	program         *os.File
	item            supply.Item
	files           []*os.File
	executables     []Executable
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closing = true
	if r.closeErr != nil {
		return r.closeErr
	}
	if r.store != nil {
		if err := r.store.Close(); err != nil {
			return err
		}
		r.store = nil
	}
	for _, projection := range r.projections {
		if err := projection.closeParentOwnedV1(); err != nil {
			r.closeErr = errors.Join(r.closeErr, err)
		}
	}
	if r.closeErr != nil {
		return r.closeErr
	}
	if r.protectedSuite != nil {
		if err := r.protectedSuite.closeLockedV1(); err != nil {
			return err
		}
	}
	for index, file := range r.pendingFiles {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil {
			r.closeErr = errors.Join(r.closeErr, err)
		} else {
			r.pendingFiles[index] = nil
		}
	}
	if r.program != nil {
		if err := r.program.Close(); err != nil {
			r.closeErr = errors.Join(r.closeErr, err)
		} else {
			r.program = nil
		}
	}
	for index, file := range r.files {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil {
			r.closeErr = errors.Join(r.closeErr, err)
		} else {
			r.files[index] = nil
		}
	}
	if r.closeErr != nil {
		return r.closeErr
	}
	r.pendingFiles, r.files, r.executables = nil, nil, nil
	r.closed = true
	return nil
}

// OwnedCommand adds physical closure evidence to the actual namespace command.
// A logical wait result alone never releases live runtime files or its lease.
type OwnedCommand interface {
	Start() error
	Wait() error
	Signal(os.Signal) error
	ExitCode() int
	PhysicalExitV1() bool
}

// Options joins the same host's protected runtime issuer and gvisor-purpose
// namespace command. Command consumes the exact typed mount identities; it
// must not re-resolve them as unrelated raw path grants.
type Options struct {
	// ExpectedRuntime reads the ORIGINAL authenticated client continuation or
	// its protected operation record. Saved OCI/layout/inventory is not a source.
	ExpectedRuntime func(context.Context) (supply.Manifest, error)
	Domain          func(context.Context) (cachefs.Domain, error)
	Command         func(context.Context, CommandRequest) (OwnedCommand, error)
	Artefacts       string
}

// Executable is one original-runtime-checked file, projected read-only at a
// fixed target inside the namespace. It is never an extra guest descriptor.
type Executable struct {
	Target string
	File   *os.File
}

// CommandRequest carries only owned file capabilities and the one canonical
// typed process mount plan to the existing gvisor-purpose jail owner.
type CommandRequest struct {
	Topology    *NativeTopologyV1
	Suite       *ProtectedSuiteV1
	ForkMounts  *ForkMountsV1
	Spec        jail.Spec
	Mounts      []wall.Mount
	MountFiles  []*os.File
	Executables []Executable
	Log         *os.File
}

type Driver struct{ options Options }

func New(options Options) (*Driver, error) {
	if options.ExpectedRuntime == nil || options.Domain == nil || options.Command == nil {
		return nil, failure("wall_setup", "gvisor needs its protected runtime and namespace owners", nil)
	}
	if options.Artefacts == "" {
		options.Artefacts = paths.ArtefactsDir()
	}
	if !filepath.IsAbs(options.Artefacts) || filepath.Clean(options.Artefacts) != options.Artefacts {
		return nil, failure("wall_setup", "gvisor artefact storage must be an absolute canonical path", nil)
	}
	return &Driver{options: options}, nil
}
func (*Driver) Name() string      { return "gvisor" }
func (*Driver) Level() wall.Level { return wall.GVisor }

// Register installs the implementation. Probe independently requires a real
// bounded smoke run under the same protected runtime and namespace owner.
func Register(options Options) error {
	d, err := New(options)
	if err != nil {
		return err
	}
	return wall.Register(d)
}

func (d *Driver) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	return d.prepare(ctx, spec, false)
}

func (d *Driver) Open(ctx context.Context, dir string) (wall.Sandbox, error) {
	spec, ok := wall.ExpectedSpec(ctx)
	if !ok || spec.SessionDir != dir {
		return nil, failure("wall_setup", "opening gvisor needs the current trusted session plan", nil)
	}
	return d.prepare(ctx, spec, true)
}

func (d *Driver) prepare(ctx context.Context, spec wall.Spec, existing bool) (wall.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !api.ValidSessionID(spec.ID) || !filepath.IsAbs(spec.SessionDir) || filepath.Clean(spec.SessionDir) != spec.SessionDir || !filepath.IsAbs(spec.Helper) || spec.Image != nil || spec.VM != nil || spec.Root == wall.Keep {
		return nil, failure("wall_setup", "gvisor needs a trusted private session, helper and fresh process filesystem", nil)
	}
	storage, err := prepareStorage(ctx, spec, existing)
	if storage == nil {
		return nil, failure("wall_setup", "cannot prepare gvisor runtime storage", err)
	}
	resource := &sandbox{driver: d, spec: spec, owner: ctx, storage: storage, done: make(chan struct{}), exit: wall.Exit{Code: -1}, preparationErr: err}
	if err != nil {
		return resource, failure("wall_setup", "cannot prepare gvisor runtime storage", err)
	}
	return resource, nil
}

// ConstructionProbeV1 reports supported construction facts without executing
// an installed runtime or asserting physical readiness. The original selected
// runtime purpose separately owns the bounded smoke command.
func ConstructionProbeV1(ctx context.Context) wall.Probe {
	p := wall.Probe{Driver: "gvisor", Level: wall.GVisor, Facts: map[string]string{"platform": "systrap"}}
	if err := ctx.Err(); err != nil {
		p.Reason, p.Fix = err.Error(), "Run the sandbox probe again."
		return p
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		p.Reason = "gvisor needs Linux amd64 or arm64"
		p.Fix = "Use a supported Linux host."
		return p
	}
	var uname unix.Utsname
	if err := unix.Uname(&uname); err != nil {
		p.Reason = "cannot read the host kernel"
		p.Fix = "Run host doctor before selecting gvisor."
		return p
	}
	release := unix.ByteSliceToString(uname.Release[:])
	p.Facts["kernel"] = release
	if !supportsKernel(release) {
		p.Reason = "gvisor needs Linux 5.6 or later"
		p.Fix = "Update the host kernel."
		return p
	}
	p.CanPrepare = true
	p.Reason = "physical gvisor readiness needs this connection's original delivered runtime"
	p.Fix = "Prepare the selected signed runtime, then run its physical probe on the same connection."
	return p
}

func (d *Driver) Probe(ctx context.Context) wall.Probe {
	// Execution needs an original caller that can retain a real failed probe.
	// A display-only Driver interface has no resource-return channel.
	return ConstructionProbeV1(ctx)
}

// ProbeOwnedV1 returns the actual physical probe resource even on failure.
// The original host probe purpose must adopt it before handling Probe.OK.
func (d *Driver) ProbeOwnedV1(ctx context.Context) (result wall.Probe, resource wall.Sandbox) {
	p := ConstructionProbeV1(ctx)
	if !p.CanPrepare {
		return p, resource
	}
	spec, ok := wall.ExpectedSpec(ctx)
	if !ok || spec.ID != "gvisorprobe" || spec.Root != wall.Reset || spec.Image != nil || spec.VM != nil {
		return p, resource
	}
	// These are the factory's private native-purpose projections. A supplied
	// Spec alone cannot obtain either the original runtime or its host domain.
	if _, err := d.options.ExpectedRuntime(ctx); err != nil {
		p.Reason, p.Fix = err.Error(), "Prepare the selected runtime on the same authenticated connection."
		return p, resource
	}
	if _, err := d.options.Domain(ctx); err != nil {
		p.Reason, p.Fix = err.Error(), "Retain the original host account and physical probe owner."
		return p, resource
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	storage, err := prepareStorage(probe, spec, false)
	if storage != nil {
		s := &sandbox{driver: d, spec: spec, owner: probe, storage: storage, done: make(chan struct{}), exit: wall.Exit{Code: -1}, smoke: true, preparationErr: err}
		resource = s
		if err == nil {
			_, err = s.Start(probe)
			if err == nil {
				result := s.Wait()
				err = result.Err
				if err == nil && result.Code != 0 {
					err = fmt.Errorf("runsc smoke exited with code %d", result.Code)
				}
			}
		}
		cleanupErr := s.Destroy()
		err = errors.Join(err, cleanupErr)
		p.Version = s.version
	}
	if err != nil {
		p.Reason = err.Error()
		p.Fix = "Run host prepare, then host doctor, before selecting gvisor."
		var supplied *proto.Error
		if errors.As(err, &supplied) {
			p.Fix = supplied.Hint
		}
		return p, resource
	}
	p.OK, p.Reason, p.Fix = true, "", ""
	return p, resource
}

func supportsKernel(release string) bool {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major > 5 || major == 5 && minor >= 6
}

type sandbox struct {
	topology                                                     *NativeTopologyV1
	forkMounts                                                   *ForkMountsV1
	nativeLive                                                   *LiveOwnerV1
	driver                                                       *Driver
	spec                                                         wall.Spec
	owner                                                        context.Context
	storage                                                      *runtimeStorage
	life                                                         sync.Mutex
	mu                                                           sync.Mutex
	command                                                      OwnedCommand
	connection                                                   net.Conn
	cancel                                                       context.CancelFunc
	done                                                         chan struct{}
	exit                                                         wall.Exit
	started, released, destroyed, physical, forced, hello, smoke bool
	version                                                      string
	runtime                                                      *Runtime
	preparationErr                                               error
}

func (s *sandbox) Abilities() wall.Abilities {
	s.mu.Lock()
	owner := s.nativeLive
	s.mu.Unlock()
	return wall.Abilities{Freeze: owner.FreezeAvailableV1()}
}
func (s *sandbox) CleanExit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started && s.physical && s.hello && !s.forced && s.exit.Err == nil && s.exit.Code == 0
}

var _ wall.Driver = (*Driver)(nil)
var _ wall.Sandbox = (*sandbox)(nil)
