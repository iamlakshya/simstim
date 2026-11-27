package api

// ImagePolicyValues selects a laptop-side cosign certificate policy. The root
// path belongs to the laptop; none of these values grant host or boot authority.
type ImagePolicyValues struct {
	// Identity is the exact cosign certificate identity required for OCI images.
	Identity *string `json:"identity,omitempty" toml:"identity,omitempty"`
	// Issuer is the exact OIDC issuer required for that identity.
	Issuer *string `json:"issuer,omitempty" toml:"issuer,omitempty"`
	// TrustedRoot names a Sigstore trusted_root.json on the laptop. Empty uses
	// the public Sigstore trusted root embedded in the client.
	TrustedRoot *string `json:"trusted_root,omitempty" toml:"trusted_root,omitempty"`
}
