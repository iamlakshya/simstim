package egress

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestTrustedUseProjectionMatchesCanonicalRoleWithoutNewTextCaps(t *testing.T) {
	for _, endpoint := range []string{
		"https://API.example.test/v1/messages#",
		"https://api.example.test:" + strings.Repeat("0", 2048) + "443/base/chat/completions",
		"https://api.example.test/" + strings.Repeat("x", 1<<16) + "/messages",
		"https://api.example.test/a/../messages", "https://api.example.test/messages?",
		"http://api.example.test/messages", "https://api.example.test:444/messages",
	} {
		use := api.TrustedSecretUseV1{Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound,
			Model: strings.Repeat("model-label", 1024), Endpoint: endpoint,
			Bind: api.SecretBind{Hosts: []string{" API.example.test ", "api.example.test:443"}, Header: "authorization", Format: "Basic " + strings.Repeat("user", 1024) + ":{value}"}}
		wanted, fullErr := canonicalTrustedUse("KEY", use)
		raw, err := json.Marshal(use)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := api.NewJSONBytesProjection(t.Context(), raw)
		if err != nil {
			t.Fatal(err)
		}
		root, err := projection.ObjectRoot()
		if err != nil {
			t.Fatal(err)
		}
		schema := NewTrustedUseProjectionSchemaV1()
		for _, retain := range []bool{false, true} {
			actual, err := schema.Project(t.Context(), projection, root, retain)
			if (err == nil) != (fullErr == nil) {
				t.Fatalf("typed/projected role validity differs endpointlen=%d retained=%v: full=%v projected=%v", len(endpoint), retain, fullErr, err)
			}
			if retain && fullErr == nil && !reflect.DeepEqual(actual, wanted) {
				t.Fatal("retained projection changed canonical metadata")
			}
		}
	}
}
