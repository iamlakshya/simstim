package supervisor

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, proto.FeaturePackageManagersV1) }

// prepareBootPackageSettings consumes the original canonical merged policy.
// The once-per-run plan is not recomputed from a later live policy edit.
func (s *Supervisor) prepareBootPackageSettings(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root := s.owner()
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.bootPackageSettingsPrepared {
		return nil
	}
	if root.m == nil {
		return wallError("the package settings have no original session", nil)
	}
	if root.m.Wall == nil || root.m.Wall.Level == "none" {
		root.bootPackageSettingsPrepared = true
		return nil
	}
	plan, err := bootPackageSettingsPlan(root.m.Policy, now().UTC())
	if err != nil {
		return err
	}
	root.bootPackageSettings = plan
	root.bootPackageSettingsPrepared = true
	return nil
}

func bootPackageSettingsPlan(policy *api.Policy, started time.Time) (*proto.PackageManagersV1, error) {
	if policy == nil {
		return nil, wallError("the original merged package policy is unavailable", nil)
	}
	plan := &proto.PackageManagersV1{Version: 1, Mode: proto.DefaultPackageManagerMode, MinReleaseAge: proto.DefaultPackageReleaseAge, StartedAt: started.UTC().Format(time.RFC3339Nano)}
	if policy.Sandbox.PackageManagers != nil {
		plan.Mode = *policy.Sandbox.PackageManagers
	}
	if policy.Network.MinReleaseAge != nil {
		plan.MinReleaseAge = *policy.Network.MinReleaseAge
	}
	if err := proto.ValidatePackageManagersV1(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Supervisor) bootPackageSettingsArgs(args *proto.CtlStartArgs) error {
	root := s.owner()
	root.mu.Lock()
	defer root.mu.Unlock()
	if !root.bootPackageSettingsPrepared {
		return wallError("the cold-start package settings were not prepared", nil)
	}
	if root.m != nil && root.m.Wall != nil && root.m.Wall.Level != "none" && root.bootPackageSettings == nil {
		return wallError("the supported wall has no cold-start package settings", nil)
	}
	args.PackageManagers = proto.ClonePackageManagersV1(root.bootPackageSettings)
	return nil
}

func (s *Supervisor) requirePackageManagerGuest(ctx context.Context) error {
	root := s.owner()
	root.mu.Lock()
	required := root.bootPackageSettings != nil
	root.mu.Unlock()
	if !required {
		return nil
	}
	var raw json.RawMessage
	if err := s.ctl.request(ctx, proto.CtlGuestCapabilities, struct{}{}, &raw); err != nil {
		return err
	}
	var features proto.CtlGuestFeatures
	if !api.ExactJSONFields(raw, features) || json.Unmarshal(raw, &features) != nil || features.Version != 1 || !slices.Contains(features.Features, proto.FeaturePackageManagersV1) {
		return wallError("the actual guest cannot apply the cold-start package settings", nil)
	}
	root.mu.Lock()
	root.bootPackageSettingsGuest = true
	root.mu.Unlock()
	return nil
}

func (s *Supervisor) markPackageSettingsInstalled() error {
	root := s.owner()
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.bootPackageSettings == nil {
		return nil // The actual none/legacy constructor has no guest settings.
	}
	if !root.bootPackageSettingsGuest || !root.bootPackageSettingsPrepared {
		return wallError("the package settings have no matching guest preparation", nil)
	}
	root.bootPackageSettingsInstalled = true
	return nil
}

func (s *Supervisor) packageSettingsView() (*proto.PackageManagersV1, error) {
	root := s.owner()
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.rec.phase != host.LifeRunning || !root.bootPackageSettingsInstalled || root.lifetime == nil || root.lifetime.Err() != nil {
		return nil, nil
	}
	plan := proto.ClonePackageManagersV1(root.bootPackageSettings)
	if err := proto.ValidatePackageManagersV1(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Supervisor) packageSettingsRequest(c *client, request proto.Request) bool {
	if request.Op != "packages.view" {
		return false
	}
	if len(request.Args) > 0 && (!api.ExactJSONFields(request.Args, struct{}{}) || json.Unmarshal(request.Args, &struct{}{}) != nil) {
		c.reply(request.ID, nil, toProtoErrOrNil(wallError("invalid installed package settings request", nil)))
		return true
	}
	view, err := s.packageSettingsView()
	c.reply(request.ID, view, toProtoErrOrNil(err))
	return true
}
