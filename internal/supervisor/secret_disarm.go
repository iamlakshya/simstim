package supervisor

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

// disarmPublicSecretsLocked records the loss of egress's real-value memory.
// Public stand-ins/history survive, while unsupported opaque state stays exact.
func disarmPublicSecretsLocked(manifest *host.Manifest) bool {
	if manifest == nil || manifest.SecretState == nil || !manifest.SecretState.Supported() {
		return false
	}
	state := api.CloneSessionSecretState(manifest.SecretState)
	changed := false
	for name, public := range state.Primary {
		if public.Armed {
			public.Armed = false
			state.Primary[name] = public
			changed = true
		}
	}
	if changed {
		manifest.SecretState = state
	}
	return changed
}
