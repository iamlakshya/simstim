package egress

import (
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

const (
	CtlBoundInit       = "init.bound"
	CtlArm             = "arm"
	MemberAddV2Feature = "member-add-v2"
	MemberRegistryV2   = 2
	EventSecretMinted  = "secret_minted"
)

// BoundInitArgs is a negotiated v2 form. Initial keeps the existing policy,
// DNS and saved kind set. Only its Secrets map carries transient values.
type BoundInitArgs struct {
	TrustedUses                  map[string]api.TrustedSecretUseV1    `json:"trusted_uses,omitempty"`
	ProtectedTrustedSecretValues map[string]api.Secret                `json:"protected_trusted_secret_values,omitempty"`
	Version                      int                                  `json:"version"`
	Initial                      InitArgs                             `json:"initial"`
	Modes                        map[string]api.SecretMode            `json:"modes"`
	Binds                        map[string]api.SecretBind            `json:"binds,omitempty"`
	Saved                        map[string]api.PublicSecretState     `json:"saved,omitempty"`
	SavedMinted                  map[string][]PublicMintedSecretState `json:"saved_minted,omitempty"`
	// SourceKinds is format provenance for exactly the fresh bound values.
	// It grants no name, builtin, binding, fetch or destination permission.
	SourceKinds  map[string][]string                        `json:"source_kinds,omitempty"`
	SavedRetired map[string][]api.RetiredPrimarySecretState `json:"saved_retired,omitempty"`
}

// BoundInitResult contains only public trust and public sentinel metadata.
type BoundInitResult struct {
	TrustedUses map[string]api.TrustedSecretUseV1          `json:"trusted_uses,omitempty"`
	Version     int                                        `json:"version"`
	CACert      string                                     `json:"ca_cert"`
	Generation  string                                     `json:"generation"`
	Secrets     map[string]api.PublicSecretState           `json:"secrets"`
	Minted      map[string][]PublicMintedSecretState       `json:"minted,omitempty"`
	Retired     map[string][]api.RetiredPrimarySecretState `json:"retired,omitempty"`
}

// MemberAddV2Args contains exactly the newly approved member's values/intent.
// Existing members receive no environment injection.
type MemberAddV2Args struct {
	TrustedUses                  map[string]api.TrustedSecretUseV1 `json:"trusted_uses,omitempty"`
	ProtectedTrustedSecretValues map[string]api.Secret             `json:"protected_trusted_secret_values,omitempty"`
	Version                      int                               `json:"version"`
	Generation                   string                            `json:"generation"`
	Kind                         string                            `json:"kind"`
	Values                       map[string]api.Secret             `json:"values,omitempty"`
	Modes                        map[string]api.SecretMode         `json:"modes,omitempty"`
	Binds                        map[string]api.SecretBind         `json:"binds,omitempty"`
}

// MemberAddResultV2 keeps public persistence metadata in the same successful
// transaction. Its names are exactly the requested member's names.
type MemberAddResultV2 struct {
	TrustedUses map[string]api.TrustedSecretUseV1 `json:"trusted_uses,omitempty"`
	Version     int                               `json:"version"`
	Generation  string                            `json:"generation"`
	Env         map[string]api.Secret             `json:"env"`
	Secrets     map[string]api.PublicSecretState  `json:"secrets"`
}

// ArmArgs changes only values of existing bound names at the exact epoch.
type ArmArgs struct {
	TrustedUses                  map[string]api.TrustedSecretUseV1 `json:"trusted_uses,omitempty"`
	ProtectedTrustedSecretValues map[string]api.Secret             `json:"protected_trusted_secret_values,omitempty"`
	Version                      int                               `json:"version"`
	Generation                   string                            `json:"generation"`
	Values                       map[string]api.Secret             `json:"values"`
}

// ArmResult never returns real values or redaction history.
type ArmResult struct {
	TrustedUses map[string]api.TrustedSecretUseV1 `json:"trusted_uses,omitempty"`
	Version     int                               `json:"version"`
	Generation  string                            `json:"generation"`
	Secrets     map[string]api.PublicSecretState  `json:"secrets"`
}

func (BoundInitArgs) Format(f fmt.State, _ rune)     { _, _ = io.WriteString(f, api.Redacted) }
func (BoundInitResult) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, api.Redacted) }
func (MemberAddV2Args) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, api.Redacted) }
func (MemberAddResultV2) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (ArmArgs) Format(f fmt.State, _ rune)           { _, _ = io.WriteString(f, api.Redacted) }
func (ArmResult) Format(f fmt.State, _ rune)         { _, _ = io.WriteString(f, api.Redacted) }
