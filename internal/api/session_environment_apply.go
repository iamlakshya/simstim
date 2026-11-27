package api

import (
	"maps"
	"slices"
	"strings"
)

// SessionEnvironmentError is a low-level validation error, not a wire object.
// Actual Host/Sup callers wrap its message/code/hint at their protocol boundary.
type SessionEnvironmentError struct{ Code, Message, Hint string }

func (e *SessionEnvironmentError) Error() string { return e.Message + "; Try: " + e.Hint }

func sessionEnvironmentError(message string) error {
	return &SessionEnvironmentError{Code: "bad_env", Message: message, Hint: "Resolve ordinary variables through the original local configuration and secret-name checks before starting the session."}
}

// ApplySessionEnvironment applies a COMPLETE resolved ordinary environment to
// a freshly reconstructed runtime base. The base MUST NOT be Manifest.Env or
// another previously merged environment. This function reads no host variable,
// interprets no directive, and creates no second ordinary-value store.
// The actual caller applies protected identity/BROWSER/CA/proxy controls last.
func ApplySessionEnvironment(freshBase map[string]string, environment *SessionEnvironment, state *SessionSecretState, legacyNames []string) (map[string]string, error) {
	if environment == nil || environment.Version != SessionEnvironmentV1 || environment.Values == nil {
		return nil, sessionEnvironmentError("the complete ordinary environment is unavailable")
	}
	if state != nil && !state.Supported() {
		return nil, sessionEnvironmentError("the saved public secret namespace is unsupported")
	}
	for name, value := range environment.Values {
		if CheckSecretName(name, true) != SecretNameOK || strings.ContainsRune(value, 0) {
			return nil, sessionEnvironmentError("an ordinary environment entry is invalid")
		}
		if name == "SSH_AUTH_SOCK" || slices.Contains(legacyNames, name) || savedEnvironmentSecretName(state, name) {
			return nil, &SessionEnvironmentError{Code: "env_is_secret", Message: "an ordinary variable collides with the protected secret namespace", Hint: "Pass secret values through the canonical secret delivery flow."}
		}
	}
	result := maps.Clone(freshBase)
	if result == nil {
		result = map[string]string{}
	}
	for name, value := range environment.Values {
		result[name] = value
	}
	return result, nil
}

func savedEnvironmentSecretName(state *SessionSecretState, name string) bool {
	if state == nil {
		return false
	}
	if _, found := state.Intent.Modes[name]; found {
		return true
	}
	if _, found := state.Intent.Binds[name]; found {
		return true
	}
	if _, found := state.Primary[name]; found {
		return true
	}
	if _, found := state.Minted[name]; found {
		return true
	}
	_, found := state.Retired[name]
	return found
}
