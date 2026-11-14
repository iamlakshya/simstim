package proto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/secretmode"
)

func TestCtlSecretModesPreserveAbsentAndExplicitEmptyForms(t *testing.T) {
	args := CtlStartArgs{Session: "store234", Argv: []string{}, Env: []string{}, Cwd: "/work"}
	legacy, err := json.Marshal(args)
	if err != nil || strings.Contains(string(legacy), "secret_modes") {
		t.Fatal("legacy marshal gained new security metadata")
	}
	args.SecretModes = map[string]secretmode.SecretMode{}
	explicit, err := json.Marshal(args)
	if err != nil || !strings.Contains(string(explicit), `"secret_modes":{}`) {
		t.Fatal("explicit empty mode form was omitted or changed to null")
	}
	args.Secrets = map[string]string{"BOUND_KEY": "public-sentinel", "EXPOSED_KEY": "explicit-value"}
	args.SecretModes = map[string]secretmode.SecretMode{"BOUND_KEY": secretmode.SecretModeBound, "EXPOSED_KEY": secretmode.SecretModeExposed}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CtlStartArgs
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.SecretModes["BOUND_KEY"] != secretmode.SecretModeBound || decoded.SecretModes["EXPOSED_KEY"] != secretmode.SecretModeExposed {
		t.Fatal("actual common start/spawn DTO lost delivery modes")
	}
}
