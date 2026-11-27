package api

import "encoding/hex"

// CodeGrantQuestion asks the client to review hash-bound repository proposals.
const CodeGrantQuestion = "code_grant"

// RepositoryPolicyFeature identifies the trusted file preflight and exact-hash
// recheck contract. Older helpers must not silently ignore grant answers.
const RepositoryPolicyFeature = "repository-policy-file-v1"

// RepositoryPolicy is read from the host's original working tree before any
// wall exists. It contains policy data only, never commands or secret values.
type RepositoryPolicy struct {
	Repo      string   `json:"repo"`
	Hash      string   `json:"hash"`
	Layer     Layer    `json:"layer"`
	Proposals []Rule   `json:"proposals,omitempty"`
	Notes     []string `json:"notes,omitempty"`
}

// GrantQuestion shows every proposed allowance for one exact file version.
// Answers are indexed by canonical rule ID and apply only to Hash.
type GrantQuestion struct {
	Repo      string   `json:"repo"`
	Hash      string   `json:"hash"`
	Proposals []Rule   `json:"proposals"`
	Notes     []string `json:"notes,omitempty"`
}

// ValidPolicyHash accepts the lower-case hexadecimal SHA-256 of file bytes.
func ValidPolicyHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}
