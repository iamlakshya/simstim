package client

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// PolicyStart is a materialised policy before secret values are fetched.
// The host receives only the layers, never the client's tier or confirmation.
type PolicyStart struct {
	Layers           []api.Layer
	Policy           api.Policy
	RepositoryPolicy *api.RepositoryPolicy
}

// ResolvePolicyStart reads the host folder's actual origin through repo.info.
// A scratch folder has no repository sections. An unavailable helper or another
// repository error is refused rather than silently dropping a possible lock.
func ResolvePolicyStart(ctx context.Context, h Host, dir, profile string, session api.LayerValues) (PolicyStart, error) {
	settings, err := ReadPolicySettings()
	if err != nil {
		return PolicyStart{}, err
	}
	origin := ""
	repo, err := SessionRepoPolicy(ctx, h, dir)
	if err != nil {
		var ue *UserError
		if !errors.As(err, &ue) || ue.Code != "not_a_repo" {
			return PolicyStart{}, err
		}
	} else {
		origin = repo.Origin
	}
	layers, err := ResolvePolicyLayers(settings, h, origin, profile, session)
	if err != nil {
		return PolicyStart{}, err
	}
	selectedProfile := ""
	for _, layer := range layers {
		if layer.Kind == api.LayerProfile {
			selectedProfile = layer.Name
		}
	}
	for i := range layers {
		if layers[i].Kind == api.LayerSession {
			profile := selectedProfile
			layers[i].Sandbox.Profile = &profile
		}
	}
	policy, clean, err := egress.MergeLayers(layers)
	if err != nil {
		return PolicyStart{}, policyError(err)
	}
	return PolicyStart{Layers: clean, Policy: policy, RepositoryPolicy: repo.Policy}, nil
}

// PrepareNewPolicy materialises or refreshes the laptop's named layers before
// wall resolution, grant checks or secret fetches. Repeating it keeps the
// session flags and origin-section names while observing newly added locks.
func (c *Conn) PrepareNewPolicy(ctx context.Context, a NewArgs) (NewArgs, error) {
	settings, err := ReadPolicySettings()
	if err != nil {
		return a, err
	}
	if len(a.Layers) == 0 {
		values := api.LayerValues{Sandbox: api.SandboxValues{Mounts: slices.Clone(a.Mounts), Include: slices.Clone(a.Include), Secrets: secretMapNames(a.Secrets)}}
		if a.Wall != "" {
			wall := a.Wall
			values.Sandbox.Wall = &wall
		}
		start, err := ResolvePolicyStart(ctx, c.Host, a.Cwd, "", values)
		if err != nil {
			return a, err
		}
		a.Layers = start.Layers
		a.RepositoryPolicy = start.RepositoryPolicy
	} else {
		layers, err := RefreshPolicyLayers(c.Host, a.Layers)
		if err != nil {
			return a, err
		}
		a.Layers = layers
	}
	var repoErr error
	a, repoErr = c.prepareRepositoryPolicy(ctx, a)
	if repoErr != nil {
		return a, repoErr
	}
	if err := c.CheckHTTPMethodFeature(StartPolicyLayers(a)); err != nil {
		return a, err
	}
	policy, clean, err := egress.MergeLayers(StartPolicyLayers(a))
	if err != nil {
		return a, policyError(err)
	}
	a.Layers = laptopStartLayers(clean)
	if !a.Downgrade && policy.Sandbox.Wall != nil {
		level := *policy.Sandbox.Wall
		// A configured level below the floor is raised rather than treated as
		// an explicit request to waive it. Explicit --wall is checked below.
		if !sessionHasWall(a.Layers) {
			level = raisedConfigWall(level, c.Host.WallFloor)
		}
		a.Wall = level
	}
	if len(settings.Repos) > 0 || a.Wall != "" && a.Wall != "none" || layersRequireWall(StartPolicyLayers(a)) {
		if err := c.CheckPolicyLayerFeature(a.Layers); err != nil {
			return a, err
		}
	}
	if (a.Wall == "" || a.Wall == "none" || a.Wall == "auto" && (c.Host.WallFloor == "" || c.Host.WallFloor == "none")) && layersRequireWall(StartPolicyLayers(a)) {
		return a, &UserError{Msg: "sandbox policy needs a walled session", Hint: "Start with --wall process or a stronger available level.", Code: "not_walled", Kind: KindRefused}
	}
	a.Mounts = slices.Clone(policy.Sandbox.Mounts)
	a.Include = slices.Clone(policy.Sandbox.Include)
	return a, nil
}

func sessionHasWall(layers []api.Layer) bool {
	for _, layer := range layers {
		if layer.Kind == api.LayerSession && layer.Sandbox.Wall != nil && *layer.Sandbox.Wall != "auto" {
			return true
		}
	}
	return false
}
func raisedConfigWall(level, floor string) string {
	if level == "auto" {
		return level
	}
	asked, err := ParseWallFloor(level)
	if err != nil {
		return level
	}
	minimum, err := ParseWallFloor(floor)
	if err == nil && asked < minimum {
		return floor
	}
	return level
}

// PolicyWallNote explains a configured level raised to the actual host floor.
// An explicit named session level retains the canonical downgrade refusal.
func PolicyWallNote(h Host, layers []api.Layer) string {
	if sessionHasWall(layers) {
		return ""
	}
	policy, _, err := egress.MergeLayers(layers)
	if err != nil || policy.Sandbox.Wall == nil || *policy.Sandbox.Wall == "auto" {
		return ""
	}
	asked, err := ParseWallFloor(*policy.Sandbox.Wall)
	if err != nil {
		return ""
	}
	floor, err := ParseWallFloor(h.WallFloor)
	if err != nil || asked >= floor {
		return ""
	}
	return fmt.Sprintf("The config asks for %s; using %s, the host floor.", asked, floor)
}

// PolicySecretNames returns the effective named secrets after list additions
// and removals. It never reads or fetches their values.
func PolicySecretNames(layers []api.Layer) ([]string, error) {
	policy, _, err := egress.MergeLayers(layers)
	if err != nil {
		return nil, policyError(err)
	}
	return slices.Clone(policy.Sandbox.Secrets), nil
}

func layersRequireWall(layers []api.Layer) bool {
	for _, layer := range layers {
		if len(layer.Rules) > 0 || layer.Lock != nil {
			return true
		}
		values := layer.LayerValues
		values.Sandbox.Wall = nil
		values.Sandbox.Profile = nil
		values.Sandbox.Mounts = nil
		values.Sandbox.Include = nil
		values.Sandbox.Secrets = nil
		if !reflect.ValueOf(values).IsZero() {
			return true
		}
	}
	return false
}

// selectedPolicySecrets removes values which a refreshed layer no longer
// selects, and refuses a newly selected name before anything can be sent.
func selectedPolicySecrets(layers []api.Layer, values map[string]api.Secret) (map[string]api.Secret, error) {
	names, err := PolicySecretNames(layers)
	if err != nil {
		return nil, err
	}
	out := make(map[string]api.Secret, len(names))
	for _, name := range names {
		value, ok := values[name]
		if !ok {
			return nil, &UserError{Msg: "the current policy needs its selected secrets", Hint: "Start again to fetch the current policy's names.", Code: "secrets_missing", Kind: KindFailed}
		}
		out[name] = value
	}
	return out, nil
}
