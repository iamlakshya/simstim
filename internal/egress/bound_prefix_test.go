package egress

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundSelectedPrefixDoesNotBecomeUnverifiedGlobalAuthority(t *testing.T) {
	const name, prefix = "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-"
	custom, customInitial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{name: "ABCD0000"}))
	old := custom.currentMembers()
	if old.secrets[name].bound.shape.Prefix != "" {
		t.Fatal("unverified selected-agent prefix became a custom global default")
	}
	scope := boundRequestScope(t, custom, []string{name})
	intent := memberIntent(customInitial.Generation, map[string]api.Secret{name: "ABCD0000"}, "claude")
	if _, err := custom.addBoundControlMember(memberJSON(t, intent)); err == nil || old != custom.currentMembers() || scope.ctx.Err() != nil {
		t.Fatal("incompatible selected member replaced a live generic stand-in or disturbed its owner")
	}
	args := boundRegistryArgs(map[string]string{name: prefix + strings.Repeat("A", 20)})
	args.Initial.Agent = "claude"
	args.SourceKinds[name] = []string{"claude"}
	selected, initial := newBoundRegistryServer(t, args)
	if initial.Secrets[name].Shape.Prefix != prefix || !strings.HasPrefix(string(initial.Secrets[name].Sentinel), prefix) {
		t.Fatal("selected normative prefix was discarded along with filtered global metadata")
	}
	if _, err := selected.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{name: api.Secret(prefix + strings.Repeat("B", 20))}})); err != nil || selected.currentMembers().secrets[name].bound.sentinel != initial.Secrets[name].Sentinel {
		t.Fatal("primary arm changed a live selected-prefix stand-in")
	}
}

func TestBoundColdRestoreUsesSavedSourceShapeAcrossDifferentRootAgent(t *testing.T) {
	const name, prefix = "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-"
	args := boundRegistryArgs(map[string]string{name: prefix + strings.Repeat("A", 20)})
	args.Initial.Agent = "claude"
	args.SourceKinds[name] = []string{"claude"}
	_, saved := newBoundRegistryServer(t, args)
	args.Initial.Agent = "shell"
	args.Initial.Secrets = nil
	args.SourceKinds = nil
	args.Saved = saved.Secrets
	restored, result := newBoundRegistryServer(t, args)
	if result.Secrets[name].Armed || !reflect.DeepEqual(result.Secrets[name].Shape, saved.Secrets[name].Shape) || !strings.HasPrefix(string(result.Secrets[name].Sentinel), prefix) || result.Secrets[name].Sentinel == saved.Secrets[name].Sentinel {
		t.Fatal("cold restore inferred the root agent's default instead of preserving the saved source format")
	}
	if restored.currentMembers().secrets[name].value != "" {
		t.Fatal("saved public format recovered or fetched a real value")
	}
	args.Initial.Secrets = map[string]string{name: prefix + strings.Repeat("B", 20)}
	args.SourceKinds = map[string][]string{name: {"shell"}}
	_, refreshed := newBoundRegistryServer(t, args)
	if !refreshed.Secrets[name].Armed || !reflect.DeepEqual(refreshed.Secrets[name].Shape, saved.Secrets[name].Shape) {
		t.Fatal("fresh approved value could not use its exact persisted source format")
	}
	args.Initial.Secrets[name] = prefix + strings.Repeat("B", 21)
	if _, err := prepareBoundInitial(t.Context(), args); err == nil {
		t.Fatal("cold fresh value silently recompiled a different shape over saved public state")
	}
}

func TestBoundRestoreRejectsObservedPrivatePrefixForAnUnrelatedConfiguredName(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"CREDENTIAL": "ABCD0000"})
	args.Initial.Secrets = nil
	args.SourceKinds = nil
	args.Saved = map[string]api.PublicSecretState{"CREDENTIAL": {Mode: api.SecretModeBound, Sentinel: "ABCD0000", Identity: strings.Repeat("a", 32), Shape: &api.SecretShape{Version: 1, Prefix: "AB", Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Count: 2, Width: 1}, {Class: "ascii-digit", Count: 4, Width: 1}}}}}
	if _, err := prepareBoundInitial(t.Context(), args); err == nil {
		t.Fatal("an observed private prefix became a canonical saved format without source provenance")
	}
}
