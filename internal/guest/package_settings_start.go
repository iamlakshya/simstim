package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"

	"github.com/iamlakshya/simstim/internal/proto"
)

type packageSettingsPhase uint8

const (
	packageSettingsUnprepared packageSettingsPhase = iota
	packageSettingsApplying
	packageSettingsApplied
	packageSettingsFailed
)

func (g *Init) validateBootPackageSettings(plan *proto.PackageManagersV1) error {
	if err := proto.ValidatePackageManagersV1(plan); err != nil {
		return err
	}
	if g.packageSettingsRequired && plan == nil {
		return packageManagerFailure("the supported wall has no cold-start package settings")
	}
	return nil
}

// applyBootPackageSettings delegates the fixed home writes to the actual
// existing UID1000 owned-command runner, before setup or agent commands.
func (g *Init) applyBootPackageSettings(ctx context.Context, plan *proto.PackageManagersV1) (returned error) {
	if ctx == nil && (plan != nil || g.packageSettingsRequired) {
		return packageManagerFailure("the package settings writer has no guest lifetime")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := g.validateBootPackageSettings(plan); err != nil {
		return err
	}
	g.mu.Lock()
	if g.packageSettingsPhase == packageSettingsApplied {
		equal := reflect.DeepEqual(g.packageSettingsPlan, plan)
		g.mu.Unlock()
		if !equal {
			return packageManagerFailure("the prepared package settings changed")
		}
		return nil
	}
	if g.packageSettingsPhase != packageSettingsUnprepared || g.ended {
		g.mu.Unlock()
		return packageManagerFailure("the package settings installation is occupied or failed")
	}
	if ctx == nil {
		g.mu.Unlock()
		if plan == nil && !g.packageSettingsRequired {
			return nil // Existing literal legacy/none fixtures have no lifetime.
		}
		return packageManagerFailure("the package settings writer has no guest lifetime")
	}
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return err
	}
	g.packageSettingsPhase = packageSettingsApplying
	g.mu.Unlock()
	defer func() {
		if returned != nil {
			g.mu.Lock()
			g.packageSettingsPhase = packageSettingsFailed
			g.mu.Unlock()
		}
	}()
	if plan != nil && plan.Mode != "off" {
		executable, err := g.helperExecutable()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(plan)
		if err != nil || len(payload) > proto.PackageManagerPayloadMax {
			return packageManagerFailure("the private package settings plan exceeds its bound")
		}
		run := newOwnedCommand(ctx, proto.CtlExecArgs{Argv: []string{executable, PackageManagerHelperMode}, Cwd: "/"}, inputCommandEnv())
		run.vmRole = vmSeedCommand
		defer run.cancel()
		code := g.executeOwned(run, ownedCommandIO{Out: io.Discard, Err: io.Discard, AttachInput: func(input io.WriteCloser) func() {
			done := make(chan struct{})
			go func() { defer close(done); defer input.Close(); _, _ = io.Copy(input, bytes.NewReader(payload)) }()
			return func() { _ = input.Close(); <-done }
		}})
		if err := ctx.Err(); err != nil {
			return err
		}
		if code != 0 {
			return packageManagerFailure("the fixed-account package settings helper did not complete")
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.ended {
		return packageManagerFailure("the guest ended before the package settings installation completed")
	}
	g.packageSettingsPlan = proto.ClonePackageManagersV1(plan)
	g.packageSettingsPhase = packageSettingsApplied
	return nil
}

func (g *Init) checkBootPackageSettings(plan *proto.PackageManagersV1) error {
	if err := g.validateBootPackageSettings(plan); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.packageSettingsPhase == packageSettingsUnprepared && plan == nil && !g.packageSettingsRequired {
		return nil
	}
	if g.packageSettingsPhase != packageSettingsApplied || !reflect.DeepEqual(g.packageSettingsPlan, plan) {
		return packageManagerFailure("the member changed its installed package settings")
	}
	return nil
}
