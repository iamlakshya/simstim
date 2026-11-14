package host

import (
	"encoding/json"
	"io"
	"math"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func secretSummaryFrom(m *Manifest) *api.SessionSecretSummary {
	if m.SecretState == nil {
		return nil
	}
	unknown := &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Unsupported: true}
	if egress.ValidateSavedPublicSessionState(m.SecretState) != nil {
		return unknown
	}
	state := m.SecretState
	if m.SandboxOf != "" {
		if !api.ValidSessionID(m.SandboxOf) || m.SandboxOf == m.ID {
			return unknown
		}
		root, err := LoadManifest(m.SandboxOf)
		if err != nil || root.ID != m.SandboxOf || root.SandboxOf != "" || root.Wall == nil || egress.ValidateSavedPublicSessionState(root.SecretState) != nil {
			return unknown
		}
		for _, name := range m.Secrets {
			if root.SecretState.Intent.Modes[name] != state.Intent.Modes[name] {
				return unknown
			}
		}
		state = root.SecretState
	}
	return api.SummariseSessionSecrets(state, m.Secrets)
}

// MemberPublicationInfo publishes a fresh member's explicit start result.
// Ordinary member rows use MemberInfo and contain only the compact summary.
func MemberPublicationInfo(m *Manifest, alive bool, root ...*Manifest) (*api.SessionInfo, error) {
	info := publicationInfoFrom(m, alive, root...)
	if err := checkSecretPublicationResponse(info); err != nil {
		return nil, err
	}
	return &info, nil
}

func checkSecretPublicationResponse(value any) error {
	payload, err := json.Marshal(value)
	if err == nil {
		err = proto.NewWriter(io.Discard).WriteJSON(proto.TResponse, proto.Response{ID: math.MaxUint64, OK: true, Result: payload})
	}
	if err != nil {
		return UserError("state_too_large", "the complete session publication cannot fit in its response frame",
			"List sessions to find the session that started. Reduce its retained public metadata before starting another session.")
	}
	return nil
}

// publicationInfoFrom is used only by explicit start/resume results. Ordinary
// list and watch rows carry the compact summary from infoFrom instead.
func publicationInfoFrom(m *Manifest, alive bool, roots ...*Manifest) SessionInfo {
	owned := CloneManifest(*m)
	info := infoFrom(&owned, alive)
	info.SecretState = api.CloneSessionSecretState(owned.SecretState)
	if publicationRunCoherent(&owned, roots) {
		info.RunGeneration = owned.Run.Generation
	}
	return info
}

func publicationRunCoherent(m *Manifest, roots []*Manifest) bool {
	if m.Wall == nil || !api.ValidSessionID(m.ID) || m.Run.Generation == "" || m.Run.BootID == "" || m.Run.StartedAt.IsZero() {
		return false
	}
	if m.SecretState != nil && egress.ValidateSavedPublicSessionState(m.SecretState) != nil {
		return false
	}
	if m.SandboxOf == "" {
		return true
	}
	if !api.ValidSessionID(m.SandboxOf) || len(roots) != 1 || roots[0] == nil {
		return false
	}
	root := roots[0]
	if root.Wall == nil || root.SandboxOf != "" || root.ID != m.SandboxOf || root.ID == m.ID || root.Run.Generation != m.Run.Generation || root.Run.StartedAt.IsZero() || root.Run.BootID == "" || root.Run.BootID != m.Run.BootID {
		return false
	}
	if m.SecretState != nil && (root.SecretState == nil || egress.ValidateSavedPublicSessionState(root.SecretState) != nil || root.SecretState.Generation != m.SecretState.Generation) {
		return false
	}
	return true
}
