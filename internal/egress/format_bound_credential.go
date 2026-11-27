package egress

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// FormatBoundCredential formats a caller-owned value through the same bound
// HTTP codec. It fetches no value and supplies no name, source, destination or
// publication authority. A local capability must keep the result inside its
// exact header write and must not persist or return that header to a caller.
func FormatBoundCredential(binding api.SecretBind, value api.Secret) (string, error) {
	canonical, err := secretbind.Canonical(binding)
	if err != nil || !validBoundCredential(value) {
		return "", memberError("secret_format", "credential cannot use its approved HTTP format", "Use an exact HTTPS binding and a nonempty value without header controls.")
	}
	kind, user, err := secretbind.ParseFormat(canonical.Format)
	if err != nil {
		return "", registryIntentError()
	}
	field, valid := requestCredentialValue(kind, user, value)
	if !valid || len(field)+len(canonical.Header)+4 > maxHTTPHeaderBytes {
		return "", memberError("secret_format", "credential exceeds its approved HTTP header representation", "Use a representable bound credential and header format.")
	}
	return string(field), nil
}
