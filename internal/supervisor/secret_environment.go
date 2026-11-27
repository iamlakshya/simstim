package supervisor

import (
	"fmt"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type secretEnvironment struct {
	Env     map[string]string
	Modes   map[string]api.SecretMode
	Missing []string
}

func (secretEnvironment) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func secretIntentFailure() error {
	return &proto.Error{Code: "secret_intent", Message: "the approved secret delivery intent is unsupported or inconsistent",
		Hint: "Use matching helpers and the original locally approved secret names. Do not fall back to exposure."}
}

func knownSessionSecretState(state *api.SessionSecretState) error {
	if state != nil && !state.Supported() {
		return &proto.Error{Code: "secret_state", Message: "the saved public secret state needs a matching helper",
			Hint: "Update the client and host helpers before passing fresh values. Keep the saved state unchanged."}
	}
	return nil
}

// secretEnvironmentFor selects only this requested process's environment.
// Availability warnings follow its fresh-value presence, not public stand-ins.
func secretEnvironmentFor(names []string, intent api.SecretDeliveryIntent, public map[string]api.PublicSecretState, actual map[string]string) (secretEnvironment, error) {
	if intent.Version != api.SecretDeliveryIntentV1 {
		return secretEnvironment{}, secretIntentFailure()
	}
	result := secretEnvironment{Env: make(map[string]string), Modes: make(map[string]api.SecretMode)}
	for _, name := range names {
		mode, declared := intent.Modes[name]
		state, known := public[name]
		if !declared || !mode.Valid() || !known || state.Mode != mode {
			return secretEnvironment{}, secretIntentFailure()
		}
		value, supplied := actual[name]
		if !supplied {
			result.Missing = append(result.Missing, name)
		}
		switch mode {
		case api.SecretModeBound:
			if state.Sentinel == "" || state.Shape == nil || supplied && state.Sentinel == api.Secret(value) {
				return secretEnvironment{}, secretIntentFailure()
			}
			result.Env[name], result.Modes[name] = string(state.Sentinel), mode
		case api.SecretModeExposed:
			if supplied {
				result.Env[name], result.Modes[name] = value, mode
			}
		default:
			return secretEnvironment{}, secretIntentFailure()
		}
	}
	slices.Sort(result.Missing)
	result.Missing = slices.Compact(result.Missing)
	return result, nil
}

func dropBoundValues(values map[string]string, modes map[string]api.SecretMode) {
	for name, mode := range modes {
		if mode == api.SecretModeBound {
			delete(values, name)
		}
	}
}
