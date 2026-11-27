package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestNativeWarmResumeClosedWrapperAcceptsItsObjectRequest(t *testing.T) {
	raw := json.RawMessage(`{"v":1,"scope":{"v":1,"primary":"primary2","nonce":"` + strings.Repeat("a", 64) + `"},"request":{"id":"primary2"}}`)
	wire, args, err := decodeNativeWarmResumeV1(raw)
	if err != nil || wire.Scope.Primary != "primary2" || args.ID != "primary2" {
		t.Fatal("the closed raw wrapper rejected its ordinary object request", err)
	}
}
func TestNativeWarmResumeRefusesUnknownDuplicateAndNamedPayloads(t *testing.T) {
	base := `{"v":1,"scope":{"v":1,"primary":"primary2","nonce":"` + strings.Repeat("a", 64) + `"},"request":{"id":"primary2"}}`
	for _, raw := range []string{
		strings.Replace(base, `"v":1,`, `"v":1,"v":1,`, 1),
		strings.Replace(base, `"scope":{`, `"scope":{"other":true,`, 1),
		strings.Replace(base, `"request":{`, `"request":{"named_ssh_keys":{"private":"value"},`, 1),
		strings.Replace(base, `"request":{`, `"request":{"ID":"primary2",`, 1),
		strings.Replace(base, `"request":{`, `"request":{"id":"changed2",`, 1),
		strings.Replace(base, `"scope":{`, `"scope":{"V":1,`, 1),
		strings.Replace(base, `"request":{`, `"other":true,"request":{`, 1),
	} {
		if _, _, err := decodeNativeWarmResumeV1(json.RawMessage(raw)); err == nil {
			t.Fatal("unsupported warm declaration accepted", raw)
		}
	}
}
func TestNativeWarmScopeCannotOriginateFromLogicalState(t *testing.T) {
	lifetime, cancel := context.WithCancel(t.Context())
	cancel()
	for _, scope := range []*nativeStopScopeV1{nil, {}, {purpose: proto.NativeResumeInspectV1, root: &Supervisor{}, owner: &host.OwnedSupervisorLease{}, lifetime: lifetime}} {
		if err := scope.checkCurrentV1(t.Context()); err == nil {
			t.Fatal("logical state or ended lifetime supplied a native warm owner")
		}
	}
}
