package egress

import (
	"encoding/base64"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSecretStoreFormatsAndClose(t *testing.T) {
	store := NewSecretStore()
	err := store.Arm([]BoundCredential{
		{
			Route:  "anthropic",
			Name:   "ANTHROPIC_API_KEY",
			Header: "x-api-key",
			Format: "{value}",
			Value:  api.Secret("sk-ant-test-123"),
		},
		{
			Route:  "github",
			Name:   "GITHUB_TOKEN",
			Header: "Authorization",
			Format: "basic:x-access-token:{value}",
			Value:  api.Secret("ghp_test_456"),
		},
	})
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}

	name, hdr, val, ok := store.Lookup("anthropic")
	if !ok || name != "ANTHROPIC_API_KEY" || hdr != "X-Api-Key" || val != "sk-ant-test-123" {
		t.Fatalf("anthropic lookup = (%q, %q, %q, %v)", name, hdr, val, ok)
	}

	name, hdr, val, ok = store.Lookup("github")
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_test_456"))
	if !ok || name != "GITHUB_TOKEN" || hdr != "Authorization" || val != wantBasic {
		t.Fatalf("github lookup = (%q, %q, %q, %v), want %q", name, hdr, val, ok, wantBasic)
	}

	// Unbound route fails closed.
	if _, _, _, ok := store.Lookup("other"); ok {
		t.Fatal("unexpected lookup hit for unbound route")
	}

	store.Close()
	if _, _, _, ok := store.Lookup("anthropic"); ok {
		t.Fatal("secret survived Close")
	}
}
