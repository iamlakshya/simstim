package host

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// ApplyResumeLayers replaces only the laptop sources already named by the
// trusted manifest. Session flags/rules and the host's repository-file source
// survive. No source names are invented on resume, including for a legacy manifest.
func ApplyResumeLayers(m *Manifest, fresh []api.Layer) error {
	if IsNoneSupervisor(m) {
		return checkNoneResumeCarriersV1(m, fresh)
	}
	if len(fresh) == 0 {
		return nil
	}
	if m.Wall == nil {
		return UserError("not_walled", "only a walled session may receive policy layers", "Resume this session without policy layers.")
	}
	expected := map[api.LayerRef]bool{}
	for _, layer := range m.Layers {
		if laptopLayer(layer) {
			expected[api.LayerRef{Kind: layer.Kind, Name: layer.Name}] = true
		}
	}
	incoming := map[api.LayerRef]api.Layer{}
	kinds := map[api.LayerKind]bool{}
	for _, layer := range fresh {
		ref := api.LayerRef{Kind: layer.Kind, Name: layer.Name}
		if !laptopLayer(layer) || kinds[layer.Kind] && layer.Kind != api.LayerRepo {
			return badResumeLayer()
		}
		if _, duplicate := incoming[ref]; duplicate {
			return badResumeLayer()
		}
		if !expected[ref] {
			return badResumeLayer()
		}
		incoming[ref] = layer
		kinds[layer.Kind] = true
	}
	for ref := range expected {
		if _, ok := incoming[ref]; !ok {
			return badResumeLayer()
		}
	}
	next := slices.Clone(m.Layers)
	for i, layer := range next {
		ref := api.LayerRef{Kind: layer.Kind, Name: layer.Name}
		if replacement, ok := incoming[ref]; ok && laptopLayer(layer) {
			next[i] = replacement
			delete(incoming, ref)
		}
	}
	policy, clean, err := egress.MergeLayers(next)
	if err != nil {
		return err
	}
	m.Policy, m.Layers = &policy, clean
	m.Secrets = slices.Clone(policy.Sandbox.Secrets)
	return nil
}

func laptopLayer(layer api.Layer) bool {
	if layer.Source != "" {
		return false
	}
	switch layer.Kind {
	case api.LayerAll, api.LayerHost, api.LayerRepo, api.LayerProfile:
		return true
	default:
		return false
	}
}

func badResumeLayer() error {
	return UserError("bad_request", "resume layers must name exactly the saved laptop sources", "Use the saved repository and profile names from policy ls.")
}
