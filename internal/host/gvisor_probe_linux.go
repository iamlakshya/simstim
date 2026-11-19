//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

type gvisorProbeKeyV1 struct{}

// This private purpose is issued only by the real bridge after the same
// connection's actual successful runtime delivery. It has no session manifest,
// SessionOperation, original boot receipt, Need or startup/stop capability.
type gvisorProbePurposeV1 struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	bridge         *bridge
	request        uint64
	selected       *selectedRuntimeReceiptV1
	root           *os.Root
	rootRef        cachefs.Reference
	parent         *os.Root
	parentRef      cachefs.Reference
	name           string
	lease          *os.File
	leaseRef       cachefs.Reference
	spec           wall.Spec
	command        *probeGvisorCommandV1
	issued, closed bool
}

func (b *bridge) gvisorProbeContextV1(req proto.Request, expected supply.Manifest) (context.Context, func() error, error) {
	if b == nil || b.ctx == nil || req.Op != "sandbox.probe" || req.ID == 0 {
		return nil, nil, newRuntimeError("the physical probe has no actual authenticated bridge request")
	}
	if err := errors.Join(CheckCaller(), b.ctx.Err()); err != nil {
		return nil, nil, err
	}
	var original sandboxProbeArgsV1
	if !api.ExactJSONFields(req.Args, original) || json.Unmarshal(req.Args, &original) != nil || original.Runtime == nil || original.ReleaseURL != "" || !reflect.DeepEqual(*original.Runtime, expected) {
		return nil, nil, newRuntimeError("the physical probe differs from its actual decoded bridge request")
	}
	if _, err := gvisor.RuntimeItemsV1(expected, runtime.GOARCH); err != nil {
		return nil, nil, err
	}
	selected, err := b.runtime.selectedRuntime(expected)
	if err != nil {
		return nil, nil, err
	}
	return newGvisorProbePurposeV1(b.ctx, b, req.ID, selected, expected)
}

func newGvisorProbePurposeV1(ctx context.Context, b *bridge, requestID uint64, selected *selectedRuntimeReceiptV1, expected supply.Manifest) (context.Context, func() error, error) {
	var err error
	// Keep the native publication, domain and every original role alive until
	// the physical probe owner has joined its exact namespace init.
	selected.mu.RLock()
	if err := selected.checkLocked(ctx); err != nil || !reflect.DeepEqual(selected.manifest, expected) {
		selected.mu.RUnlock()
		return nil, nil, errors.Join(newRuntimeError("the physical probe lost its original delivered runtime"), err)
	}
	owned, cancel := context.WithCancel(ctx)
	purpose := &gvisorProbePurposeV1{ctx: owned, cancel: cancel, bridge: b, request: requestID, selected: selected}
	fail := func(cause error) (context.Context, func() error, error) {
		return nil, nil, errors.Join(cause, purpose.close())
	}
	purpose.parent, err = cachefs.OpenPrivateRoot(paths.RuntimeDir(), true)
	if err != nil {
		return fail(err)
	}
	purpose.parentRef, err = cachefs.CaptureRoot(purpose.parent)
	if err != nil {
		return fail(err)
	}
	purpose.name = ".gvisor-probe-" + rand.Text()
	if err := purpose.parent.Mkdir(purpose.name, 0o700); err != nil {
		return fail(err)
	}
	dir := filepath.Join(paths.RuntimeDir(), purpose.name)
	purpose.spec = wall.Spec{ID: "gvisorprobe", SessionDir: dir, Root: wall.Reset}
	purpose.root, err = purpose.parent.OpenRoot(purpose.name)
	if err != nil {
		return fail(err)
	}
	purpose.rootRef, err = cachefs.CaptureRoot(purpose.root)
	if err != nil {
		return fail(err)
	}
	purpose.lease, err = cachefs.OpenFile(purpose.root, "probe.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fail(err)
	}
	locked, err := cachefs.TryLock(purpose.lease, cachefs.Exclusive)
	if err != nil || !locked {
		return fail(errors.Join(newRuntimeError("the physical probe has no original native lease"), err))
	}
	purpose.leaseRef, err = cachefs.CaptureFile(purpose.root, "probe.lock", purpose.lease, 0o600)
	if err != nil {
		return fail(err)
	}
	helper, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	purpose.spec.Helper = filepath.Dir(helper)
	manifest := cloneSelectedRuntime(expected)
	purpose.spec.RuntimeManifest = &manifest
	scope := context.WithValue(owned, gvisorProbeKeyV1{}, purpose)
	return wall.WithExpectedSpec(scope, purpose.spec), purpose.close, nil
}

func gvisorProbeFromContextV1(ctx context.Context) *gvisorProbePurposeV1 {
	if ctx == nil {
		return nil
	}
	purpose, _ := ctx.Value(gvisorProbeKeyV1{}).(*gvisorProbePurposeV1)
	return purpose
}

func (p *gvisorProbePurposeV1) check(ctx context.Context) error {
	if p == nil || ctx == nil {
		return newRuntimeError("the physical probe has no private purpose")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.bridge == nil || p.request == 0 || p.selected == nil || p.root == nil || p.lease == nil {
		return newRuntimeError("the physical probe's native owner ended")
	}
	if err := errors.Join(ctx.Err(), p.ctx.Err(), p.bridge.ctx.Err()); err != nil {
		return err
	}
	if err := p.selected.checkLocked(ctx); err != nil {
		return err
	}
	if err := p.rootRef.CheckRoot(p.root); err != nil {
		return err
	}
	if err := p.parentRef.CheckRoot(p.parent); err != nil {
		return err
	}
	if err := p.leaseRef.CheckFile(p.root, "probe.lock", p.lease, 0o600); err != nil {
		return err
	}
	current, err := cachefs.OpenPrivateRoot(p.spec.SessionDir, false)
	if err != nil {
		return err
	}
	return errors.Join(p.rootRef.CheckRoot(current), current.Close())
}

func gvisorExpectedRuntimeV1(ctx context.Context) (supply.Manifest, error) {
	if purpose := gvisorProbeFromContextV1(ctx); purpose != nil {
		if err := purpose.check(ctx); err != nil {
			return supply.Manifest{}, err
		}
		return cloneSelectedRuntime(purpose.selected.manifest), nil
	}
	return ExpectedOriginalRuntime(ctx)
}

func gvisorNativeDomainV1(ctx context.Context) (cachefs.Domain, error) {
	if purpose := gvisorProbeFromContextV1(ctx); purpose != nil {
		if err := purpose.check(ctx); err != nil {
			return cachefs.Domain{}, err
		}
		return purpose.selected.publication.owner.domain(), nil
	}
	return OriginalBootDomain(ctx)
}

func (p *gvisorProbePurposeV1) close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	command := p.command
	p.cancel()
	p.mu.Unlock()
	var result error
	if command != nil {
		result = command.stopAndJoin()
	}
	// Only actual native exit reaches here. A timed-out wait cannot release
	// the original runtime publication or make this probe's storage reusable.
	var cleanupErr error
	if p.root != nil && p.rootRef.Known() && p.parent != nil {
		cleanupErr = errors.Join(p.rootRef.CheckRoot(p.root), p.parentRef.CheckRoot(p.parent))
		if cleanupErr == nil {
			current, err := p.parent.OpenRoot(p.name)
			if err == nil {
				err = errors.Join(p.rootRef.CheckRoot(current), current.Close())
			}
			cleanupErr = err
		}
		if cleanupErr == nil {
			cleanupErr = p.root.RemoveAll("gvisor")
			if err := p.root.Remove("gvisor.lock"); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
			if p.lease != nil && p.leaseRef.Known() {
				err := p.leaseRef.CheckFile(p.root, "probe.lock", p.lease, 0o600)
				if err == nil {
					err = p.root.Remove("probe.lock")
				}
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
	}
	if p.lease != nil {
		result = errors.Join(result, p.lease.Close())
	}
	if p.root != nil {
		result = errors.Join(result, p.root.Close())
	}
	if p.parent != nil {
		if cleanupErr == nil && p.name != "" && p.rootRef.Known() {
			cleanupErr = p.parent.Remove(p.name)
		}
		result = errors.Join(result, p.parent.Close())
	}
	if p.selected != nil {
		p.selected.mu.RUnlock()
	}
	return errors.Join(result, cleanupErr)
}

type probeGvisorCommandV1 struct {
	*jail.Cmd
	purpose *gvisorProbePurposeV1
	ctx     context.Context
	cancel  context.CancelFunc
	waitMu  sync.Mutex
	waited  bool
	waitErr error
}

func (c *probeGvisorCommandV1) Start() error {
	err := c.purpose.check(c.ctx)
	if err == nil {
		c.purpose.mu.Lock()
		if c.purpose.closed || c.purpose.issued || c.purpose.command != c {
			err = newRuntimeError("this physical probe purpose was already consumed")
		} else {
			c.purpose.issued = true
		}
		c.purpose.mu.Unlock()
	}
	if err != nil {
		c.cancel()
	}
	return errors.Join(err, c.Cmd.Start())
}

func (c *probeGvisorCommandV1) Wait() error {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	if !c.waited {
		c.waited = true
		c.waitErr = c.Cmd.Wait()
	}
	var nativeErr error
	for !c.Cmd.PhysicalExitV1() {
		if err := c.Cmd.JoinPhysicalV1(); err != nil && nativeErr == nil {
			nativeErr = err
		}
		if !c.Cmd.PhysicalExitV1() {
			// The same owner stays live on an uncertain native observation.
			// This delay is a retry interval, never an exit deadline.
			time.Sleep(100 * time.Millisecond)
		}
	}
	c.cancel()
	return errors.Join(c.waitErr, nativeErr)
}

func (c *probeGvisorCommandV1) stopAndJoin() error {
	c.cancel()
	c.purpose.mu.Lock()
	issued := c.purpose.issued
	c.purpose.mu.Unlock()
	if !issued {
		// Close captured pins through the cancelled Cmd's normal early path.
		_ = c.Cmd.Start()
	}
	if c.Cmd.PhysicalExitV1() {
		return nil
	}
	signalErr := c.Cmd.Signal(os.Kill)
	return errors.Join(signalErr, c.Wait())
}
