package api

import "testing"

func TestTrustedReadinessCannotTreatNullUnknownOrAnotherGenerationAsArmed(t *testing.T) {
	query := TrustedSecretReadinessQueryV1{Version: 1, Generation: "original", Uses: map[string]TrustedSecretUseV1{"KEY": {Version: 1}}}
	ready := TrustedSecretReadinessV1{Version: 1, Generation: "original", Unarmed: []string{}}
	if !ready.ValidFor(query) {
		t.Fatal("complete empty unarmed observation rejected")
	}
	for _, bad := range []TrustedSecretReadinessV1{{Version: 1, Generation: "original"}, {Version: 2, Generation: "original", Unarmed: []string{}}, {Version: 1, Generation: "other", Unarmed: []string{}}, {Version: 1, Generation: "original", Unarmed: []string{"OTHER"}}, {Version: 1, Generation: "original", Unarmed: []string{"KEY", "KEY"}}} {
		if bad.ValidFor(query) {
			t.Fatal("missing, unknown, stale or duplicate observation invented armed state")
		}
	}
}
