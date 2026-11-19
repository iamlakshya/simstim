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
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

// Runtime is an owned, non-serialisable execution capability. Its fields are
// private: neither an installed-runtime list nor a caller's item can mint one.
// The canonical signed-runtime opener owns construction and checks all runsc
// companions from the same original protected operation before returning it.
type Runtime struct {
	mu          sync.Mutex
	program     *os.File
	item        supply.Item
	files       []*os.File
	executables []Executable
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var result error
	for _, f := range append([]*os.File{r.program}, r.files...) {
		if f != nil {
			result = errors.Join(result, f.Close())
		}
	}
	r.program, r.files, r.executables = nil, nil, nil
	return result
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
	if err != nil {
		return nil, failure("wall_setup", "cannot prepare gvisor runtime storage", err)
	}
	return &sandbox{driver: d, spec: spec, owner: ctx, storage: storage, done: make(chan struct{}), exit: wall.Exit{Code: -1}}, nil
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
	p := ConstructionProbeV1(ctx)
	if !p.CanPrepare {
		return p
	}
	spec, ok := wall.ExpectedSpec(ctx)
	if !ok || spec.ID != "gvisorprobe" || spec.Root != wall.Reset || spec.Image != nil || spec.VM != nil {
		return p
	}
	// These are the factory's private native-purpose projections. A supplied
	// Spec alone cannot obtain either the original runtime or its host domain.
	if _, err := d.options.ExpectedRuntime(ctx); err != nil {
		p.Reason, p.Fix = err.Error(), "Prepare the selected runtime on the same authenticated connection."
		return p
	}
	if _, err := d.options.Domain(ctx); err != nil {
		p.Reason, p.Fix = err.Error(), "Retain the original host account and physical probe owner."
		return p
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	storage, err := prepareStorage(probe, spec, false)
	if err == nil {
		s := &sandbox{driver: d, spec: spec, owner: probe, storage: storage, done: make(chan struct{}), exit: wall.Exit{Code: -1}, smoke: true}
		_, err = s.Start(probe)
		if err == nil {
			result := s.Wait()
			err = result.Err
			if err == nil && result.Code != 0 {
				err = fmt.Errorf("runsc smoke exited with code %d", result.Code)
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
		return p
	}
	p.OK, p.Reason, p.Fix = true, "", ""
	return p
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
}

func (s *sandbox) Abilities() wall.Abilities { return wall.Abilities{} }
func (s *sandbox) CleanExit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started && s.physical && s.hello && !s.forced && s.exit.Err == nil && s.exit.Code == 0
}

var _ wall.Driver = (*Driver)(nil)
var _ wall.Sandbox = (*sandbox)(nil)
