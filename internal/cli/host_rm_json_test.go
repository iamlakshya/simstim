package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHostPurgeJSONOmitsNativeProofAndCleansDisplay(t *testing.T) {
	report := api.PurgeResult{HostID: "private-host-identity", Scope: "private-source-scope", Nonce: "private-removal-nonce", User: "private-native-user", UID: 123, Arch: "amd64", HostComplete: true, GuardWitness: &api.PurgeGuardWitness{Token: "private-native-token"}, Removed: []api.PurgeItem{{Kind: "unit", Path: "one\n\x1b[2Jtwo"}}}
	raw, err := json.Marshal(publicPurgeReportV1(report))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-host-identity", "private-source-scope", "private-removal-nonce", "private-native-user", "private-native-token", "guard_witness", "operation_nonce"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("native proof entered a public removal projection", private)
		}
	}
	var public hostPurgeJSONV1
	if err := json.Unmarshal(raw, &public); err != nil {
		t.Fatal(err)
	}
	if len(public.Removed) != 1 || strings.ContainsAny(public.Removed[0].Path, "\n\x1b") {
		t.Fatal("unsafe terminal bytes entered display data", public.Removed)
	}
}
