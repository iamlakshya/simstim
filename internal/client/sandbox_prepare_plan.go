package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// NewHostPrepareGrantRequest owns one fixed sandbox test-boot phase. Its
// purpose is closed and cannot donate authority to session starts, root steps,
// runtime upgrades, image saves, forge pushes or ledger grants.
func NewHostPrepareGrantRequest() *GrantRequest {
	return &GrantRequest{purpose: grantHostPrepare}
}

// HostPrepareOptionsV1 configures one fixed sandbox preparation test boot.
// Fixed guest memory, CPU, disk, timeout and command come from the producer
// contract and cannot be overridden here.
type HostPrepareOptionsV1 struct {
	// Level optionally requests a specific wall level (process, gvisor or
	// microvm). When empty, PrepareSandboxV1 chooses the strongest runnable or
	// preparable wall permitted by the host's current tier.
	Level string
	// Progress receives optional human-readable progress output from the
	// existing runtime and image producers.
	Progress io.Writer
	// RootPrinted records that the caller printed a root script instead of
	// running it. When no runnable wall exists, preparation reports an honest
	// no-wall outcome without writing a floor or cache entry.
	RootPrinted bool
}

// SandboxPrepareOutcomeV1 is an opaque preparation outcome. Public methods
// expose display data only and never authorise floor or cache publication.
type SandboxPrepareOutcomeV1 struct {
	plan           *sandboxPreparePlanV1
	conn           *Conn
	stamp          *hostTransportStamp
	tested         bool
	noRunnableWall bool
	observedLevel  string
	levelAfterRoot string
	rootPrinted    bool
	result         api.SandboxPrepareResultV1
	probe          api.CapProbe
}

// Result returns display data from the fixed test boot, or the zero value when
// no wall test boot ran.
func (o *SandboxPrepareOutcomeV1) Result() api.SandboxPrepareResultV1 {
	if o == nil || !o.tested {
		return api.SandboxPrepareResultV1{}
	}
	return o.result
}

// Tested reports whether a real fixed wall test boot and positive owned
// cleanup completed on the accepted saved connection.
func (o *SandboxPrepareOutcomeV1) Tested() bool {
	return o != nil && o.tested && !o.noRunnableWall && o.plan != nil
}

// NoRunnableWall reports that no permitted runnable wall was available on the
// host, so no test boot ran and no floor or cache write is permitted.
func (o *SandboxPrepareOutcomeV1) NoRunnableWall() bool {
	return o != nil && o.noRunnableWall && !o.tested
}

// ObservedLevel returns the actual tested wall level, or "none" when no
// permitted wall could run.
func (o *SandboxPrepareOutcomeV1) ObservedLevel() string {
	if o == nil {
		return ""
	}
	return o.observedLevel
}

// LevelAfterRoot returns the stronger level reported by api.LevelsFor after
// optional root steps, or "" when none applies.
func (o *SandboxPrepareOutcomeV1) LevelAfterRoot() string {
	if o == nil {
		return ""
	}
	return o.levelAfterRoot
}

// RootPrinted reports whether this outcome followed a printed root script.
func (o *SandboxPrepareOutcomeV1) RootPrinted() bool {
	return o != nil && o.rootPrinted
}

// Probe returns a cleaned copy of the capability probe observed for this phase.
func (o *SandboxPrepareOutcomeV1) Probe() api.CapProbe {
	if o == nil {
		return api.CapProbe{}
	}
	return CleanCapProbe(o.probe)
}

type sandboxPreparePlanV1 struct {
	mu               sync.Mutex
	request          *GrantRequest
	target           *HostPrepareTargetV1
	conn             *Conn
	stamp            *hostTransportStamp
	source           hostStateSource
	hostID           string
	operation        *settingsOperation
	requestedLevel   string
	rootPrinted      bool
	level            string
	expectedDriver   string
	runtimeName      string
	levelAfterRoot   string
	probe            api.CapProbe
	vm               *VMNeedPlanV1
	plannedWire      []byte
	callID           uint64
	busy             bool
	imagePhaseActive bool
	recording        bool
	closing          bool
	closed           bool
	sent             bool
	wrote            bool
	replied          bool
	failed           bool
	uncertain        bool
	completed        bool
	noRunnableWall   bool
	bootSucceeded    bool
	cleanupConfirmed bool
	recorded         bool
	result           api.SandboxPrepareResultV1
	activeCancel     context.CancelFunc
}

func requireHostPrepareOperation(ctx context.Context) error {
	if ctx == nil {
		return grantPurposeError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return grantPurposeError()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return err
	}
	if r.purpose != grantHostPrepare || r.SSHAgent || r.Untrusted ||
		r.operation != grantOpUnset && r.operation != grantOpSandboxPrepare ||
		r.operation != grantOpUnset && r.operationUntrusted != r.Untrusted {
		return grantPurposeError()
	}
	r.operation, r.operationUntrusted = grantOpSandboxPrepare, r.Untrusted
	return nil
}

// imagePhaseV1 exposes this plan's retained settingsOperation ONLY while
// PrepareSandboxV1 is actively inside its fixed default-image/VM-Need phase.
// It is the exact H5 join point used by image-family-continue21-v1.
func (p *sandboxPreparePlanV1) imagePhaseV1(ctx context.Context, c *Conn) (*settingsOperation, error) {
	if ctx == nil || p == nil || c == nil {
		return nil, grantOperationError()
	}
	if err := requireHostPrepareOperation(ctx); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	p.mu.Lock()
	active := !p.closed && !p.closing && !p.failed && !p.uncertain && !p.sent && p.busy && p.imagePhaseActive
	same := p.request == r && p.conn == c && p.stamp == c.hostTransport && p.operation != nil && p.target != nil
	operation := p.operation
	target := p.target
	p.mu.Unlock()
	if !active || !same {
		return nil, grantOperationError()
	}
	if err := target.checkConn(ctx, c); err != nil {
		return nil, err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return nil, err
	}
	return operation, nil
}

// PrepareSandboxV1 runs one fixed sandbox test boot on an accepted saved
// connection belonging to target under a NewHostPrepareGrantRequest context.
// It reuses the existing signed runtime, default release image and VM Need
// producers, sends the owned sandbox.prepare request under the fixed 60-second
// lifetime, and returns an opaque outcome for RecordSandboxPreparationV1.
func (c *Conn) PrepareSandboxV1(ctx context.Context, target *HostPrepareTargetV1, options HostPrepareOptionsV1) (*SandboxPrepareOutcomeV1, error) {
	if ctx == nil {
		return nil, grantOperationError()
	}
	if err := requireHostPrepareOperation(ctx); err != nil {
		return nil, err
	}
	if target == nil || c == nil {
		return nil, sandboxPrepareError("sandbox preparation needs its original saved target and connection")
	}
	if err := target.checkConn(ctx, c); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	if !c.HasFeature("sandbox") || !c.HasFeature(api.FeatureSandboxPrepareV1) {
		return nil, c.grantHelperOld("run a fixed sandbox test boot")
	}
	if err := validateRequestedPrepareLevel(target, options.Level); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidateSettings(settings); err != nil {
		return nil, err
	}
	plan, workCtx, finish, err := acquireSandboxPreparePlanV1(ctx, r, target, c, operation, options)
	if err != nil {
		return nil, err
	}
	defer finish()

	probe, err := c.CapProbe(workCtx, "")
	if err != nil {
		plan.markFailed(false)
		return nil, err
	}
	if err := target.checkConn(workCtx, c); err != nil {
		plan.markFailed(false)
		return nil, err
	}
	if err := operation.Revalidate(workCtx); err != nil {
		plan.markFailed(false)
		return nil, err
	}
	_, afterRoot := api.LevelsFor(probe, api.RuntimeStatus{})
	if afterRoot == "none" {
		afterRoot = ""
	}
	level, runnable, err := selectSandboxPrepareLevelV1(target, c, probe, options.Level)
	if err != nil {
		plan.markFailed(false)
		return nil, err
	}
	if !runnable {
		plan.mu.Lock()
		plan.probe = probe
		plan.levelAfterRoot = afterRoot
		plan.noRunnableWall = true
		plan.completed = true
		plan.mu.Unlock()
		return &SandboxPrepareOutcomeV1{
			plan:           plan,
			conn:           c,
			stamp:          c.hostTransport,
			tested:         false,
			noRunnableWall: true,
			observedLevel:  "none",
			levelAfterRoot: afterRoot,
			rootPrinted:    options.RootPrinted,
			probe:          CleanCapProbe(probe),
		}, nil
	}
	plan.mu.Lock()
	if plan.level != "" && plan.level != level {
		plan.failed = true
		plan.mu.Unlock()
		return nil, grantOperationError()
	}
	plan.level = level
	plan.probe = probe
	plan.levelAfterRoot = afterRoot
	plan.mu.Unlock()

	args, expectedDriver, runtimeName, err := c.prepareSandboxArtifactsV1(workCtx, plan, probe, level, options.Progress)
	if err != nil {
		var confirm *scopedGrantConfirmation
		if !errors.As(err, &confirm) {
			plan.markFailed(false)
		}
		return nil, err
	}
	if err := target.checkConn(workCtx, c); err != nil {
		plan.markFailed(false)
		return nil, err
	}
	if err := operation.Revalidate(workCtx); err != nil {
		plan.markFailed(false)
		return nil, err
	}
	if err := c.CheckSavedSource(workCtx); err != nil {
		plan.markFailed(false)
		return nil, err
	}
	wire, err := json.Marshal(args)
	if err != nil {
		plan.markFailed(false)
		return nil, fmt.Errorf("encode the sandbox.prepare request: %w", err)
	}
	plan.mu.Lock()
	if plan.closing || plan.closed || plan.failed || plan.uncertain || plan.sent {
		plan.mu.Unlock()
		return nil, grantOperationError()
	}
	plan.plannedWire = wire
	plan.expectedDriver = expectedDriver
	plan.runtimeName = runtimeName
	plan.mu.Unlock()

	callCtx, cancelCall := context.WithTimeout(workCtx, time.Duration(api.SandboxPrepareTimeoutSecondsV1)*time.Second)
	defer cancelCall()
	pubCtx := context.WithValue(callCtx, sandboxPreparePublicationKeyV1{}, plan)
	var res api.SandboxPrepareResultV1
	if err := c.Call(pubCtx, "sandbox.prepare", args, &res); err != nil {
		plan.mu.Lock()
		plan.failed = true
		if plan.wrote && !plan.replied {
			plan.uncertain = true
		}
		plan.mu.Unlock()
		return nil, err
	}
	plan.mu.Lock()
	verified := plan.sent && plan.wrote && plan.replied && !plan.failed && !plan.uncertain &&
		plan.bootSucceeded && plan.cleanupConfirmed && plan.result == res
	if !verified {
		plan.failed = true
		plan.uncertain = true
		plan.mu.Unlock()
		return nil, sandboxPrepareProtocolError("the sandbox preparation response did not complete owned verification")
	}
	plan.completed = true
	plan.mu.Unlock()
	return &SandboxPrepareOutcomeV1{
		plan:           plan,
		conn:           c,
		stamp:          c.hostTransport,
		tested:         true,
		noRunnableWall: false,
		observedLevel:  res.Level,
		levelAfterRoot: afterRoot,
		rootPrinted:    options.RootPrinted,
		result:         res,
		probe:          CleanCapProbe(probe),
	}, nil
}

func acquireSandboxPreparePlanV1(ctx context.Context, r *GrantRequest, target *HostPrepareTargetV1, c *Conn, operation *settingsOperation, options HostPrepareOptionsV1) (*sandboxPreparePlanV1, context.Context, func(), error) {
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return nil, nil, nil, err
	}
	plan := r.sandboxPrepare
	if plan == nil {
		plan = &sandboxPreparePlanV1{
			request:        r,
			target:         target,
			conn:           c,
			stamp:          c.hostTransport,
			source:         target.source,
			hostID:         c.HostID(),
			operation:      operation,
			requestedLevel: options.Level,
			rootPrinted:    options.RootPrinted,
		}
		r.sandboxPrepare = plan
	}
	r.mu.Unlock()

	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.request != r || plan.target != target || plan.conn != c || plan.stamp != c.hostTransport ||
		plan.hostID != c.HostID() || plan.operation != operation ||
		plan.requestedLevel != options.Level || plan.rootPrinted != options.RootPrinted {
		return nil, nil, nil, grantOperationError()
	}
	if plan.closed || plan.closing || plan.busy || plan.imagePhaseActive || plan.recording ||
		plan.sent || plan.failed || plan.uncertain || plan.completed {
		return nil, nil, nil, grantOperationError()
	}
	workCtx, cancel := context.WithCancel(ctx)
	plan.busy = true
	plan.activeCancel = cancel
	finish := func() {
		cancel()
		plan.mu.Lock()
		plan.busy = false
		plan.imagePhaseActive = false
		plan.activeCancel = nil
		plan.mu.Unlock()
	}
	return plan, workCtx, finish, nil
}

func (p *sandboxPreparePlanV1) markFailed(uncertain bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.failed = true
	if uncertain {
		p.uncertain = true
	}
	p.mu.Unlock()
}
