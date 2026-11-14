package host

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundNewDecoderUsesExistingWallAndExactDeliveryBarriers(t *testing.T) {
	for _, raw := range []string{
		`{"wall":null}`,
		`{"wall":"none","wall":"none"}`,
		`{"secret_delivery":null}`,
		`{"secret_delivery":{"version":1,"Modes":{},"binds":{}}}`,
		`{"secret_delivery":{"version":1,"modes":{"KEY":"bound","KEY":"exposed"},"binds":{}}}`,
		`{"secret_delivery":{"version":1,"modes":{},"binds":{},"future":true}}`,
	} {
		if _, err := decodeNewArgs(json.RawMessage(raw)); err == nil {
			t.Fatal("new-form request lost a malformed wall or secret intent before lifecycle admission")
		}
	}
	if _, err := decodeNewArgs(json.RawMessage(`{"wall":"none"}`)); err != nil {
		t.Fatal("legacy unwalled request lost its existing decoder path")
	}
	if _, err := decodeNewArgs(json.RawMessage(`{"wall":"none","secret_delivery":{"version":1,"modes":{"KEY":"exposed"},"binds":{}}}`)); err != nil {
		t.Fatal("canonical explicit exposure could not reach semantic validation")
	}
}

func TestResumeDeliveryDecoderChecksEveryExplicitMemberIntent(t *testing.T) {
	for _, raw := range []string{
		`{"id":"aaaa2222","secret_delivery":null}`,
		`{"id":"aaaa2222","members":[{"id":"bbbb2222","secret_delivery":null}]}`,
		`{"id":"aaaa2222","members":[{"id":"bbbb2222","secret_delivery":{"version":1,"modes":{},"Binds":{}}}]}`,
		`{"id":"aaaa2222","members":[{"id":"bbbb2222","secret_delivery":{"version":1,"modes":{},"modes":{},"binds":{}}}]}`,
	} {
		if err := validateResumeSecretShape([]byte(raw)); err == nil {
			t.Fatal("resume's reconstructed member bundle lost explicit malformed intent")
		}
	}
	if err := validateResumeSecretShape([]byte(`{"id":"aaaa2222","members":[]}`)); err != nil {
		t.Fatal("legacy complete member envelope was reinterpreted as bound intent")
	}
}

func TestResumeIntentNeverUsesPublicMetadataAsImplicitDelivery(t *testing.T) {
	m := &Manifest{Secrets: []string{"KEY"}, SecretState: initialSecretState(&api.SecretDeliveryIntent{
		Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{"KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{},
	})}
	if _, err := prepareResumeSecretDelivery(m, nil, map[string]api.Secret{"KEY": "fixture"}); err == nil {
		t.Fatal("public saved mode silently substituted for the actual freshly approved intent")
	}
	intent := api.CloneSecretDeliveryIntent(m.SecretState.Intent)
	if owned, err := prepareResumeSecretDelivery(m, &intent, map[string]api.Secret{"KEY": "fixture"}); err != nil || owned == &intent {
		t.Fatal("supported explicit intent was refused or kept caller-owned storage")
	}
}
