package api

// MemberResumeArgs carries only one saved child's legacy approved values.
// Root identity and complete membership are pinned by SandboxSnapshot.
type MemberResumeArgs struct {
	// ProtectedTrustedSecretValues is private trusted-caller delivery, never guest environment.
	ProtectedTrustedSecretValues map[string]Secret     `json:"protected_trusted_secret_values,omitempty"`
	SecretDelivery               *SecretDeliveryIntent `json:"secret_delivery,omitempty"`
	ID                           string                `json:"id"`
	Secrets                      map[string]Secret     `json:"secrets,omitempty"`
}
