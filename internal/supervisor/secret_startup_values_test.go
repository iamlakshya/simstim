package supervisor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func boundStartupIntent(name string) *api.SecretDeliveryIntent {
	return &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{name: api.SecretModeBound},
		Binds: map[string]api.SecretBind{name: {Hosts: []string{"example.com:443"}, Header: "Authorization", Format: "Bearer {value}"}}}
}

func startupValueFixture() (host.Manifest, []*host.Manifest, startupSecretEnvelope) {
	root := host.Manifest{ID: "root2222", Agent: host.AgentInfo{Kind: "shell"}}
	child := &host.Manifest{ID: "child222", SandboxOf: root.ID, Agent: host.AgentInfo{Kind: "claude"}, Secrets: []string{"CREDENTIAL"}}
	message := startupSecretEnvelope{SecretDelivery: &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}},
		Members: []api.MemberResumeArgs{{ID: child.ID, Secrets: map[string]api.Secret{"CREDENTIAL": api.Secret("private-input-marker")}, SecretDelivery: boundStartupIntent("CREDENTIAL")}}}
	return root, []*host.Manifest{child}, message
}

func TestCompleteStartupUsesActualPerNameSelectedSource(t *testing.T) {
	root, members, message := startupValueFixture()
	plan, err := prepareStartupSecretInput(context.Background(), root, members, message)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Values["CREDENTIAL"] != message.Members[0].Secrets["CREDENTIAL"] || !slices.Equal(plan.SourceKinds["CREDENTIAL"], []string{"claude"}) || root.Agent.Kind != "shell" {
		t.Fatal("member-only fresh value lost its source or faked the primary Agent")
	}
	if fmt.Sprintf("%#v", *plan) != api.Redacted || fmt.Sprintf("%+v", message) != api.Redacted {
		t.Fatal("startup approval formatting exposed transient values")
	}
}

func TestSharedFreshNameRetainsEverySelectedPrefixClaimant(t *testing.T) {
	root, members, message := startupValueFixture()
	root.Secrets = []string{"CREDENTIAL"}
	root.Agent.Kind = "codex"
	message.SecretDelivery = boundStartupIntent("CREDENTIAL")
	message.AllowMissing = true
	plan, err := prepareStartupSecretInput(context.Background(), root, members, message)
	if err != nil || !slices.Equal(plan.SourceKinds["CREDENTIAL"], []string{"claude", "codex"}) || plan.Processes[root.ID].Supplied["CREDENTIAL"] {
		t.Fatal("shared fresh value lost another requested source or masked its missing input")
	}
	// Without ANY fresh value the source map must be absent. Saved shapes
	// are validated directly by the cold path, never re-inferred here.
	message.Members[0].Secrets = nil
	plan, err = prepareStartupSecretInput(context.Background(), root, members, message)
	if err != nil || plan.SourceKinds != nil || len(plan.Values) != 0 {
		t.Fatal("cold-only name fabricated fresh source provenance")
	}
}

func TestCompleteStartupRefusesConflictingSameNameValuesAndBindings(t *testing.T) {
	root, members, message := startupValueFixture()
	root.Secrets = []string{"CREDENTIAL"}
	message.SecretDelivery = boundStartupIntent("CREDENTIAL")
	message.Env = map[string]api.Secret{"CREDENTIAL": api.Secret("different-private-input")}
	if _, err := prepareStartupSecretInput(context.Background(), root, members, message); err == nil {
		t.Fatal("same configured name silently selected one member's different value")
	}
	message.Env["CREDENTIAL"] = message.Members[0].Secrets["CREDENTIAL"]
	message.SecretDelivery.Binds["CREDENTIAL"] = api.SecretBind{Hosts: []string{"other.example:443"}, Header: "Authorization", Format: "Bearer {value}"}
	if _, err := prepareStartupSecretInput(context.Background(), root, members, message); err == nil {
		t.Fatal("same configured name silently selected one different binding")
	}
}

func TestCompleteStartupRefusesMissingIntentForeignMemberAndCancellation(t *testing.T) {
	root, members, message := startupValueFixture()
	message.Members[0].SecretDelivery = nil
	if _, err := prepareStartupSecretInput(context.Background(), root, members, message); err == nil {
		t.Fatal("new-form member values fell back to legacy exposure")
	}
	root, members, message = startupValueFixture()
	members[0].SandboxOf = "other222"
	if _, err := prepareStartupSecretInput(context.Background(), root, members, message); err == nil {
		t.Fatal("foreign saved member influenced the complete startup namespace")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareStartupSecretInput(ctx, root, members, message); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled startup bundle was accepted")
	}
}

func TestCustomStartupSourceIsExplicitNoBuiltin(t *testing.T) {
	root, members, message := startupValueFixture()
	members[0].Agent.Kind = "custom-command"
	plan, err := prepareStartupSecretInput(context.Background(), root, members, message)
	if err != nil || !slices.Equal(plan.SourceKinds["CREDENTIAL"], []string{""}) {
		t.Fatal("custom command borrowed a known builtin's prefix source")
	}
}
