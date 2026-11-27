package host

import (
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

func init() {
	Features = append(Features, proto.FeatureNoneSupervisorV1)
	if err := wall.Register(none.Driver{}); err != nil {
		panic(err)
	}
}

// IsNoneSupervisor describes a recorded execution path, never native source
// authority. It does not classify an already-running legacy ordinary holder.
func IsNoneSupervisor(m *Manifest) bool {
	return m != nil && m.Wall != nil && m.Wall.Level == "none" && m.Wall.Driver == "none"
}

// NoneSupervisorShapeV1 validates canonical execution DATA. It grants no
// runtime or native source. Approved creation-floor provenance and harmless
// selectors remain DATA; actual sandbox/VM/accounting/policy roles are refused.
func NoneSupervisorShapeV1(m *Manifest) bool {
	if !IsNoneSupervisor(m) || m.path != "" || m.SandboxOf != "" || m.Files != nil || m.RuntimeManifest != nil || len(m.VMNeedV1) != 0 || m.Agents != "" || m.Auxiliary != nil || m.ResumePreparation != nil || m.Policy != nil || len(m.PolicyNext) != 0 || m.Armed || m.VMCaptureNonce != "" || m.Run.Image != "" || len(m.Run.Artefacts) != 0 || m.Run.NativeAccountMode != "" || m.Run.NativeAccountGeneration != "" || len(m.extra) != 0 || len(m.Run.extra) != 0 || m.Inputs != nil || m.CopiesPending {
		return false
	}
	if m.Run.Unit != "" && m.Run.Unit != UnitName(m.ID) {
		return false
	}
	if err := validateChosenWall(NewArgs{Wall: "none", Floor: m.Wall.Floor, Downgrade: m.Wall.DowngradedFrom != "", DowngradedFrom: m.Wall.DowngradedFrom}); err != nil {
		return false
	}
	return checkNoneResumeCarriersV1(m, m.Layers) == nil
}

// A resume keeps its creation wall and exact recorded approval. Refreshed
// selectors cannot silently extend that approval to a newly stronger wall.
func checkNoneResumeCarriersV1(m *Manifest, layers []api.Layer) error {
	if !IsNoneSupervisor(m) {
		return sessionLeaseError("the original None carrier selection changed")
	}
	approved := wall.None
	if m.Wall.DowngradedFrom != "" {
		var err error
		approved, err = wall.ParseLevel(m.Wall.DowngradedFrom)
		if err != nil || approved <= wall.None {
			return UserError("wall_floor", "the original unwalled downgrade is unsupported", "Start a new session through its original wall choice.")
		}
	}
	for _, layer := range layers {
		if layer.Sandbox.Wall == nil || *layer.Sandbox.Wall == "auto" {
			continue
		}
		asked, err := wall.ParseLevel(*layer.Sandbox.Wall)
		if err != nil {
			return UserError("bad_wall", "the refreshed unwalled selector names an unsupported wall", "Choose none, process, gvisor or microvm in the original client configuration.")
		}
		if asked > approved {
			return UserError("wall_floor", "the refreshed repository or profile requires a stronger wall than this session's original approval", "Start a new session with the required wall; this resume keeps its creation wall.")
		}
	}
	return checkNonePolicyCarriersV1(NewArgs{Wall: "none", Layers: layers, Downgrade: m.Wall.DowngradedFrom != "", DowngradedFrom: m.Wall.DowngradedFrom})
}

// UnwalledStartArgv preserves the existing hook wrapper inside the holder's
// actual PTY. It returns command data only; the original native owner starts it.
func UnwalledStartArgv(m *Manifest) []string {
	if m == nil {
		return nil
	}
	if !IsNoneSupervisor(m) {
		return slices.Clone(m.Agent.Argv)
	}
	return startArgv(slices.Clone(m.Agent.Argv), m.RunHook, hookVars(m, WasRestored(m) || wasResumed(m)), nil)
}

// None accepts only the original client's harmless wall/profile selectors.
// Actual sandbox policy is refused, rather than recorded as if it applied.
func checkNonePolicyCarriersV1(args NewArgs) error {
	if args.Runtime != nil || len(args.VMNeedV1) != 0 || args.Inputs != nil || len(args.Mounts) != 0 || len(args.Include) != 0 {
		return UserError("not_walled", "an unwalled start cannot carry sandbox runtime, mounts or copied inputs", "Choose the original admitted sandbox wall for those inputs.")
	}
	if _, _, err := egress.MergeLayers(args.Layers); err != nil {
		return err
	}
	for _, layer := range args.Layers {
		if len(layer.Sandbox.Mounts) != 0 || len(layer.Sandbox.Include) != 0 {
			return UserError("not_walled", "sandbox mounts and copied inputs do not apply to an unwalled session", "Choose a walled session for those inputs.")
		}
		if len(layer.Rules) > 0 || layer.Lock != nil {
			return UserError("not_walled", "sandbox rules and locks do not apply to an unwalled session", "Choose a walled session for those rules.")
		}
		if layer.Sandbox.Wall != nil && *layer.Sandbox.Wall != "none" && *layer.Sandbox.Wall != "auto" && !args.Downgrade {
			return UserError("wall_floor", "an unwalled start conflicts with its original wall selector", "Resolve and approve this exact wall downgrade in the original client.")
		}
		values := layer.LayerValues
		values.Sandbox.Wall = nil
		values.Sandbox.Profile = nil
		values.Sandbox.Host = nil
		values.Sandbox.Mounts = nil
		values.Sandbox.Include = nil
		values.Sandbox.Secrets = nil
		if !reflect.ValueOf(values).IsZero() {
			return UserError("not_walled", "sandbox policy does not apply to an unwalled session", "Choose a walled session for those controls.")
		}
	}
	return nil
}
