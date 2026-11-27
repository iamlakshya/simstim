package egress

import (
	"strings"
	"testing"
)

func TestResponsePinnedRuleIdentityIncludesTheExactDataBytes(t *testing.T) {
	if _, err := parsePinnedResponseRules(responseMintedData); err != nil {
		t.Fatal("embedded pinned rules do not match their exact source digest")
	}
	for _, changed := range []string{
		responseMintedData + "\n",
		strings.Replace(responseMintedData, "anthropic-admin-api-key", "anthropic-admin-api-kex", 1),
		strings.Replace(responseMintedData, "[A-Za-z0-9_-]", "[A-Za-z1-9_-]", 1),
		strings.Replace(responseMintedData, "do-not-suppress-credential-shaped-values", "suppress-credential-shaped-values", 1),
	} {
		if changed == responseMintedData {
			t.Fatal("integrity oracle did not alter its intended input")
		}
		if _, err := parsePinnedResponseRules(changed); err == nil {
			t.Fatal("edited data kept the old immutable source identity")
		}
	}
}
