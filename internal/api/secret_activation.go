package api

import (
	"fmt"
	"io"
)

// ArmSecretsArgs carries freshly authorised bound values for one live CAS.
// Neither public state nor a host row may expand its original local names.
// Every hop requires FeatureBoundSecrets before this request is sent.
type ArmSecretsArgs struct {
	TrustedUses                  map[string]TrustedSecretUseV1 `json:"trusted_uses,omitempty"`
	ProtectedTrustedSecretValues map[string]Secret             `json:"protected_trusted_secret_values,omitempty"`
	ID                           string                        `json:"id"`
	Generation                   string                        `json:"generation"`
	Secrets                      map[string]Secret             `json:"secrets"`
}

func (ArmSecretsArgs) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }
