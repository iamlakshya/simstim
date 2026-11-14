package proto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/secretmode"
)

func TestPreparedFramePreflightReservesActualClaimAndModePresence(t *testing.T) {
	args := CtlStartArgs{Session: "member234", RunToken: "run/member234", Argv: []string{"/bin/true"}, Env: []string{}, Cwd: "/work", SecretModes: map[string]secretmode.SecretMode{}}
	if err := CheckMemberPrepareFrames(CtlSpawn, args); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(CtlMemberPrepareArgs{Version: 1, Target: CtlSpawn, Start: args})
	if err != nil || !strings.Contains(string(raw), `"secret_modes":{}`) {
		t.Fatal("explicit empty modes became legacy")
	}
	args.Env = []string{"LARGE=" + strings.Repeat("x", 64<<10)}
	if err := CheckMemberPrepareFrames(CtlSpawn, args); err == nil {
		t.Fatal("oversize final Store/claim frame admitted")
	}
}
