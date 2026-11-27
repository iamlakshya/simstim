package host

import (
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// ArmSecrets forwards one freshly approved bound-only CAS to its real owner.
// It neither fetches a value nor accepts a public row as name authority.
func ArmSecrets(a api.ArmSecretsArgs) ([]byte, error) {
	if !api.ValidSessionID(a.ID) || a.Generation == "" {
		return nil, UserError("bad_request", "arm needs a session ID and its current generation", "Use a matching client and the session's full address.")
	}
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if _, err := checkSecrets(a.Secrets); err != nil {
		return nil, err
	}
	root, err := SandboxPrimaryID(a.ID)
	if err != nil {
		return nil, err
	}
	m, err := LoadManifest(root)
	if err != nil {
		return nil, err
	}
	if m.Wall == nil || m.SecretState == nil || !m.SecretState.Supported() {
		return nil, UserError("secret_delivery_required", "this session has no supported bound secret state", "Stop and resume it with matching helpers and explicit delivery modes.")
	}
	for name := range a.Secrets {
		if m.SecretState.Intent.Modes[name] != api.SecretModeBound {
			return nil, UserError("bad_secret", "arm supplied a name outside this sandbox's bound secrets", "Use only the original locally approved bound names.")
		}
	}
	return HolderRequestWithFeatures(a.ID, "arm", a, 5*time.Second, api.FeatureBoundSecrets)
}
