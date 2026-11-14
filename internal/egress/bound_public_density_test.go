package egress

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundFullPublicEnvelopeMustFitEvenWhenPrivateInitialStateFits(t *testing.T) {
	state := &memberState{version: MemberRegistryV2, generation: strings.Repeat("a", 32), metadata: InitArgs{SessionID: "bound234", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}}, kinds: []string{"shell"}, secrets: make(map[string]registeredSecret)}
	// These valid missing exposed names consume one key in the private
	// projection but two keys in public Intent plus Primary. No value or
	// guest environment entry is fabricated to exercise the density edge.
	for i := range 32 {
		prefix := fmt.Sprintf("NAME_%04d_", i)
		name := prefix + strings.Repeat("A", (64<<10)-len(prefix))
		state.secrets[name] = registeredSecret{mode: api.SecretModeExposed, identity: legacyExposedIdentity}
	}
	if err := checkBoundStateSize(t.Context(), state); err != nil {
		t.Fatal("representable private state hit an unrelated smaller name/value cap")
	}
	requireBoundError(t, checkBoundPublicStateSize(t.Context(), state), "state_too_large")
	if _, err := prepareBoundRegistry(t.Context(), state); err == nil {
		t.Fatal("mutation admitted a state whose canonical public Arm response cannot be represented")
	}
}
