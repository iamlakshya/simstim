package guest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestInputPreparationRefusesLaunchValuesModesAndUnknownFields(t *testing.T) {
	nonce, err := api.NewInputNonce()
	if err != nil {
		t.Fatal(err)
	}
	args := inputtransfer.PrepareArgs{Control: inputtransfer.Control{Scope: inputtransfer.Scope{Session: "abcd2345", Nonce: nonce, Digest: strings.Repeat("a", 64)}, CopyMax: api.DefaultCopyMax, SeedMax: api.DefaultSeedMax, Protected: []string{}}, Start: &proto.CtlStartArgs{Session: "abcd2345", Env: []string{"HOME=/home/agent"}, Cwd: "/work/project"}}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeInputPrepare(raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"secrets":{"KEY":"value"}`, `"secret_modes":{}`, `"secret_modes":null`, `"argv":["/bin/sh"]`, `"unknown":true`} {
		malformed := strings.Replace(string(raw), `"start":{`, `"start":{`+field+`,`, 1)
		if strings.HasPrefix(field, `"argv"`) {
			malformed = strings.Replace(string(raw), `"argv":null`, field, 1)
		}
		if _, err := decodeInputPrepare([]byte(malformed)); err == nil {
			t.Fatalf("accepted preparation field %s", field)
		}
	}
}
