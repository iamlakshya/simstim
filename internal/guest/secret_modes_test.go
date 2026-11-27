package guest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func requireGuestSecretCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *proto.Error
	if !errors.As(err, &failure) || failure.Code != code || failure.Hint == "" {
		t.Fatalf("want %s with a hint, got %v", code, err)
	}
}

func TestGuestSecretModesAcceptColdBoundAndDoNotProjectExposedValues(t *testing.T) {
	args := proto.CtlStartArgs{Secrets: map[string]string{"BOUND": "public-sentinel", "EXPOSED": "real-exposed-marker"}, SecretModes: map[string]api.SecretMode{"BOUND": api.SecretModeBound, "EXPOSED": api.SecretModeExposed}, Missing: []string{"BOUND", "MISSING_EXPOSED"}, Env: []string{"HOME=/home/agent"}}
	bound, err := memberBoundSentinels(args)
	if err != nil || len(bound) != 1 || bound["BOUND"] != "public-sentinel" {
		t.Fatal("cold bound projection was lost or exposed value entered Store")
	}
	if _, exists := bound["EXPOSED"]; exists {
		t.Fatal("exposed real value entered bound Store projection")
	}
	args.SecretModes = nil
	bound, err = memberBoundSentinels(args)
	if err != nil || bound != nil {
		t.Fatal("legacy form changed or gained implicit Store")
	}
}

func TestGuestSecretModeWireClosesNewFormsAndKeepsDynamicNameCase(t *testing.T) {
	for _, raw := range []string{
		`{"session":"store234","secret_modes":null}`,
		`{"session":"store234","secret_modes":{},"secret_modes":{}}`,
		`{"session":"store234","SECRET_MODES":{}}`,
		`{"session":"store234","secret_modes":{},"future":true}`,
		`{"session":"store234","secrets":{"KEY":"s"},"secret_modes":{}}`,
		`{"session":"store234","secrets":{"KEY":"s"},"secret_modes":{"OTHER":"bound"}}`,
		`{"session":"store234","secrets":{"KEY":"s"},"secret_modes":{"KEY":"BOUND"}}`,
		`{"session":"store234","secrets":{"KEY":"s"},"secret_modes":{"KEY":12345}}`,
		`{"session":"store234","secrets":{"KEY":"s"},"secret_modes":{"KEY":"bound","KEY":"exposed"}}`,
		`{"session":"store234","secrets":{"KEY":"s","KEY":"t"},"secret_modes":{"KEY":"bound"}}`,
		`{"session":"store234","secrets":{"KEY":""},"secret_modes":{"KEY":"bound"}}`,
		`{"session":"store234","secrets":{"KEY":"s\u0000t"},"secret_modes":{"KEY":"bound"}}`,
		`{"session":"store234","secrets":{"BAD-NAME":"s"},"secret_modes":{"BAD-NAME":"bound"}}`,
	} {
		_, err := (&Init{}).decodeMember([]byte(raw))
		if err == nil {
			t.Fatal("unsupported/ambiguous new delivery form was accepted")
		}
	}
	for _, raw := range []string{
		`{"session":"store234","secret_modes":{}}`,
		`{"session":"store234","secrets":{"KEY":"upper","key":"lower"},"secret_modes":{"KEY":"bound","key":"exposed"}}`,
		`{"session":"store234","legacy_unknown":true}`,
	} {
		if _, err := (&Init{}).decodeMember([]byte(raw)); err != nil {
			t.Fatalf("supported new/legacy form refused: %v", err)
		}
	}
}

func TestGuestSecretEnvCollisionRefusesBeforeStartSideEffects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uncreated-root")
	g := &Init{root: root}
	args := proto.CtlStartArgs{Session: "store234", Env: []string{"BOUND_KEY=private-user-value"}, Secrets: map[string]string{"BOUND_KEY": "public-sentinel"}, SecretModes: map[string]api.SecretMode{"BOUND_KEY": api.SecretModeBound}}
	_, err := g.startMember(args, true)
	requireGuestSecretCode(t, err, "env_is_secret")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("invalid environment reached filesystem/holder/Store setup")
	}
	for _, name := range []string{"EXPOSED_KEY", "MISSING_KEY"} {
		args = proto.CtlStartArgs{Env: []string{name + "=private-user-value"}, Secrets: map[string]string{}, SecretModes: map[string]api.SecretMode{}}
		if name == "EXPOSED_KEY" {
			args.Secrets[name] = "approved-exposed-value"
			args.SecretModes[name] = api.SecretModeExposed
		} else {
			args.Missing = []string{name}
		}
		_, err := memberBoundSentinels(args)
		requireGuestSecretCode(t, err, "env_is_secret")
	}
}

func TestGuestControllerPrepareNeverCarriesStoreModes(t *testing.T) {
	for _, mode := range []string{`{}`, `null`, `{"KEY":"bound"}`} {
		raw := []byte(`{"version":1,"start":{"session":"store234","pending_members":1,"secret_modes":` + mode + `}}`)
		if _, err := decodeControllerPrepare(raw); err == nil {
			t.Fatal("controller-only infrastructure preparation acquired a Store form")
		}
	}
}

func guestModeControlResponse(t *testing.T, g *Init, op string, args []byte) proto.Response {
	t.Helper()
	var output bytes.Buffer
	g.request(proto.NewWriter(&output), proto.Request{ID: 1, Op: op, Args: args})
	frame, err := proto.Read(bufio.NewReader(&output))
	if err != nil || frame.Type != proto.TResponse {
		t.Fatalf("control response framing: %v", err)
	}
	var response proto.Response
	if err := json.Unmarshal(frame.Payload, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestGuestActualCapabilitiesAdvertiseBoundModeImplementation(t *testing.T) {
	response := guestModeControlResponse(t, &Init{}, proto.CtlGuestCapabilities, nil)
	if !response.OK {
		t.Fatal("actual capability request failed")
	}
	var result proto.CtlGuestFeatures
	if err := json.Unmarshal(response.Result, &result); err != nil || result.Version != 1 {
		t.Fatal("capability envelope changed")
	}
	found := false
	for _, feature := range result.Features {
		found = found || feature == api.FeatureBoundSecrets
	}
	if !found {
		t.Fatal("implemented receiver failed to advertise SAME bound feature")
	}
}

func TestGuestActualStartAndSpawnRejectModeCollisionBeforeExecution(t *testing.T) {
	raw := []byte(`{"session":"store234","env":["KEY=private-value"],"secrets":{"KEY":"public"},"secret_modes":{"KEY":"bound"}}`)
	for _, op := range []string{proto.CtlStart, proto.CtlSpawn} {
		g := &Init{}
		response := guestModeControlResponse(t, g, op, raw)
		if response.OK || response.Error == nil || response.Error.Code != "env_is_secret" || g.secretStores != nil || len(g.members) != 0 || len(g.owned) != 0 {
			t.Fatal("actual start/spawn control path reached Store/holder after mode collision")
		}
	}
}
