package client

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type sandboxPreparePublicationKeyV1 struct{}

var (
	sandboxPrepareArgsFieldsV1   = []string{"version", "level", "runtime", "vm_need_v1"}
	sandboxPrepareResultFieldsV1 = []string{"version", "level", "driver", "boot_ms"}
)

func sandboxPrepareBeforeRequestV1(ctx context.Context, c *Conn, req proto.Request, _ any) error {
	plan, _ := ctx.Value(sandboxPreparePublicationKeyV1{}).(*sandboxPreparePlanV1)
	if plan == nil {
		return sandboxPrepareError("raw sandbox.prepare calls require an original host preparation plan")
	}
	if err := requireHostPrepareOperation(ctx); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return grantPurposeError()
	}
	r.mu.Lock()
	owned := r.sandboxPrepare == plan
	r.mu.Unlock()
	if !owned {
		return grantOperationError()
	}
	plan.mu.Lock()
	valid := !plan.closed && !plan.closing && !plan.failed && !plan.uncertain && !plan.sent &&
		plan.busy && !plan.imagePhaseActive && plan.callID == 0 &&
		plan.request == r && plan.conn == c && plan.stamp == c.hostTransport &&
		plan.target != nil && plan.operation != nil && len(plan.plannedWire) > 0 &&
		bytes.Equal(plan.plannedWire, req.Args)
	level := plan.level
	target := plan.target
	operation := plan.operation
	plan.mu.Unlock()
	if !valid {
		return grantOperationError()
	}
	if err := validateExactJSONObject(ctx, req.Args, sandboxPrepareArgsFieldsV1, false); err != nil {
		return sandboxPrepareProtocolError("the outgoing sandbox.prepare arguments are malformed or contain unknown fields")
	}
	var decoded api.SandboxPrepareArgsV1
	if err := json.Unmarshal(req.Args, &decoded); err != nil || decoded.Version != 1 || decoded.Level != level ||
		decoded.Level != "process" && decoded.Level != "gvisor" && decoded.Level != "microvm" {
		return sandboxPrepareProtocolError("the outgoing sandbox.prepare arguments do not match the sealed wall plan")
	}
	if err := operation.Revalidate(ctx); err != nil {
		return err
	}
	if err := target.checkConn(ctx, c); err != nil {
		return err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan.closed || plan.closing || plan.failed || plan.uncertain || plan.sent || plan.callID != 0 {
		return grantOperationError()
	}
	plan.sent = true
	plan.callID = req.ID
	return nil
}

func sandboxPrepareWroteRequestV1(ctx context.Context, c *Conn, id uint64) {
	plan, _ := ctx.Value(sandboxPreparePublicationKeyV1{}).(*sandboxPreparePlanV1)
	if plan == nil {
		return
	}
	plan.mu.Lock()
	if plan.conn == c && plan.callID == id && plan.sent {
		plan.wrote = true
	}
	plan.mu.Unlock()
}

func sandboxPrepareAfterResponseV1(ctx context.Context, c *Conn, op string, id uint64, resp proto.Response) error {
	plan, _ := ctx.Value(sandboxPreparePublicationKeyV1{}).(*sandboxPreparePlanV1)
	if plan == nil {
		return sandboxPrepareProtocolError("the sandbox.prepare reply has no original preparation operation")
	}
	plan.mu.Lock()
	matched := op == "sandbox.prepare" && plan.conn == c && plan.callID == id && resp.ID == id &&
		plan.sent && plan.wrote && !plan.replied
	if !matched {
		plan.failed = true
		plan.uncertain = true
		plan.mu.Unlock()
		return sandboxPrepareProtocolError("the sandbox.prepare reply does not match the sent request")
	}
	plan.replied = true
	if !resp.OK {
		plan.failed = true
		plan.mu.Unlock()
		return nil
	}
	wantLevel := plan.level
	wantDriver := plan.expectedDriver
	target := plan.target
	operation := plan.operation
	plan.mu.Unlock()

	failUncertain := func(err error) error {
		plan.markFailed(true)
		return err
	}
	if len(resp.Result) == 0 {
		return failUncertain(sandboxPrepareProtocolError("the host returned an empty sandbox.prepare reply"))
	}
	if err := validateExactJSONObject(ctx, resp.Result, sandboxPrepareResultFieldsV1, true); err != nil {
		return failUncertain(sandboxPrepareProtocolError("the host returned unknown, missing or duplicate fields in sandbox.prepare"))
	}
	var res api.SandboxPrepareResultV1
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return failUncertain(sandboxPrepareProtocolError("the host returned an unreadable sandbox.prepare result"))
	}
	if res.Version != 1 || res.Level != wantLevel || !validSandboxPrepareDriver(wantLevel, wantDriver, res.Driver) || res.BootMS < 0 {
		return failUncertain(sandboxPrepareProtocolError("the host sandbox.prepare result does not match the requested wall and driver"))
	}
	if err := ctx.Err(); err != nil {
		return failUncertain(err)
	}
	if err := operation.Revalidate(ctx); err != nil {
		return failUncertain(err)
	}
	if err := target.checkConn(ctx, c); err != nil {
		return failUncertain(err)
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return failUncertain(err)
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.closed || plan.closing || plan.failed || plan.uncertain {
		plan.failed = true
		plan.uncertain = true
		return grantOperationError()
	}
	plan.bootSucceeded = true
	plan.cleanupConfirmed = true
	plan.result = res
	return nil
}

func (p *sandboxPreparePlanV1) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.closing = true
	cancel := p.activeCancel
	inFlight := p.busy || p.imagePhaseActive || p.recording
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if inFlight {
		return agentModeResourceError()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy || p.imagePhaseActive || p.recording {
		return agentModeResourceError()
	}
	p.closed = true
	return nil
}

func validateExactJSONObject(ctx context.Context, raw []byte, known []string, requireAll bool) error {
	fields, err := api.ProjectedJSONObjectFields(ctx, raw, known...)
	if err != nil {
		return err
	}
	allowed := make(map[string]bool, len(known))
	for _, key := range known {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return api.ErrJSONObject
		}
	}
	if requireAll {
		for _, key := range known {
			if _, ok := fields[key]; !ok {
				return api.ErrJSONObject
			}
		}
	}
	return nil
}

func validSandboxPrepareDriver(level, expectedDriver, driver string) bool {
	if driver == "" {
		return false
	}
	if expectedDriver != "" && driver != expectedDriver {
		return false
	}
	switch level {
	case "process":
		return driver == "process"
	case "gvisor":
		return driver == "gvisor"
	case "microvm":
		return driver == "chv" || driver == "firecracker" || driver == "nitro"
	default:
		return false
	}
}
