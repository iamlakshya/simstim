//go:build linux

package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestForkTemplateHasFreshRuntimeAndNoPublicSecretGeneration(t *testing.T) {
	parent := Manifest{ID: "a2a2a2a2", Name: "work", Agent: AgentInfo{Kind: "claude", Argv: []string{"/opt/claude"}, AgentSessionID: "01234567-89ab-4cde-8123-456789abcdef", Hooks: []string{"--settings", "hooks"}, IDHistory: []IDEvent{{ID: "old"}}}, Wall: &WallRecord{Level: "process", Driver: "process"}, Files: &FilePlan{Seed: Seed{Repo: "app", Branch: brand.Name + "/a2a2a2a2/work"}}, Run: Runtime{Generation: "parent", HolderPID: 123, AgentPID: 456, Unit: "parent-unit", NativeAccountMode: "parent", NativeAccountGeneration: "parent"}, Status: Status{State: StateWorking, Claimed: true}, Guest: &GuestClaims{}, Life: &Life{}, Armed: true, SSHAgent: true, Pulled: []string{strings.Repeat("a", 40)}, SecretState: &api.SessionSecretState{Version: 1, Intent: api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"TOKEN": api.SecretModeBound}}, Generation: "parent-generation", Primary: map[string]api.PublicSecretState{"TOKEN": {Armed: true, Sentinel: "parent-sentinel"}}}, Secrets: []string{"TOKEN"}, CreatedAt: time.Unix(1, 0)}
	child, err := forkChildManifestV1(parent, "b3b3b3b3", "child")
	if err != nil {
		t.Fatal(err)
	}
	if child.ForkOf != parent.ID || child.ForkBranchOf != parent.ID || !child.ForkPending || child.ID == parent.ID || child.Name != "child" {
		t.Fatalf("bad fork identity %+v", child)
	}
	if child.Run.HolderPID != 0 || child.Run.AgentPID != 0 || child.Run.Generation != "" || child.Run.Unit != "" || child.Run.NativeAccountMode != "" || child.Armed || child.SSHAgent || child.Guest != nil || child.Life != nil {
		t.Fatal("child inherited the parent's live runtime or authority")
	}
	if child.Agent.AgentSessionID != "" || child.Agent.IDHistory != nil || child.Agent.IDSource != "" {
		t.Fatal("child inherited the parent's agent identity as its own")
	}
	if child.SecretState == nil || child.SecretState.Generation != "" || child.SecretState.Primary != nil {
		t.Fatal("child inherited the parent's public secret generation")
	}
	if !strings.Contains(strings.Join(child.Agent.Argv, " "), "--fork-session") || !strings.Contains(strings.Join(child.Agent.Argv, " "), parent.Agent.AgentSessionID) {
		t.Fatal("child did not start the parent's canonical fork conversation")
	}
	if child.Files.Seed.Branch != brand.Name+"/b3b3b3b3/work" {
		t.Fatal("child kept the parent's planned branch prefix")
	}
	child.Pulled[0] = "changed"
	child.SecretState.Intent.Modes["OTHER"] = api.SecretModeExposed
	if parent.Pulled[0] == "changed" || len(parent.SecretState.Intent.Modes) != 1 {
		t.Fatal("child policy or pull history aliases its parent")
	}
}

func TestForkNativeDTOOrZeroOwnersCannotMakeDestination(t *testing.T) {
	args, _ := json.Marshal(proto.NativeForkDestinationArgsV1{Version: 1, Scope: proto.NativeStopArgsV1{Version: 1, Primary: "a2a2a2a2", Nonce: strings.Repeat("a", 64)}, Child: "b3b3b3b3"})
	raw, _ := json.Marshal(proto.Request{ID: 20, Op: proto.NativeForkDestinationV1, Args: args})
	owner := &OwnedSupervisorLease{}
	destination, err := owner.AcceptForkDestinationV1(context.Background(), &SessionOperation{}, &StartupLineV1{}, proto.Frame{Type: proto.TRequest, Payload: raw}, proto.NativeStopStateV1{Version: 1, Primary: "a2a2a2a2", Generation: "saved"})
	if err == nil || destination != nil {
		t.Fatal("saved native data or zero owners minted a fork destination")
	}
	if err := CheckForkSnapshotRootV1(context.Background(), &SessionOperation{}, nil); err == nil {
		t.Fatal("a raw operation minted source storage authority")
	}
}

func TestForkMemberUsesSelectedCommandAndPrimaryDiskPlan(t *testing.T) {
	primary := Manifest{ID: "a2a2a2a2", Agent: AgentInfo{Kind: "claude"}, Files: &FilePlan{Seed: Seed{Repo: "app", Branch: brand.Name + "/a2a2a2a2/work"}}, Wall: &WallRecord{Level: "process", Driver: "process"}, Pulled: []string{strings.Repeat("a", 40)}, Limits: Limits{MemoryMax: 1234}}
	member := Manifest{ID: "b3b3b3b3", SandboxOf: primary.ID, Name: "shell", Agent: AgentInfo{Kind: "shell", Argv: []string{"/bin/sh", "-l"}}, Cwd: "/work/app", Wall: &WallRecord{Level: "process", Driver: "process"}, Env: []string{"MEMBER=selected"}}
	template, err := forkSelectedTemplateV1(primary, member)
	if err != nil {
		t.Fatal(err)
	}
	child, err := forkChildManifestV1(template, "c4c4c4c4", "child")
	if err != nil {
		t.Fatal(err)
	}
	child.ForkOf = member.ID
	if child.Agent.Kind != "shell" || child.Agent.Argv[0] != "/bin/sh" || child.Cwd != member.Cwd || child.ForkOf != member.ID || child.ForkBranchOf != primary.ID {
		t.Fatal("member fork substituted the primary command or branch parent")
	}
	if child.Files.Seed.Repo != "app" || child.Files.Seed.Branch != brand.Name+"/c4c4c4c4/work" || child.Limits.MemoryMax != primary.Limits.MemoryMax || len(child.Pulled) != 1 {
		t.Fatal("member fork did not retain the primary disk plan and pull history")
	}
	if _, err := forkSelectedTemplateV1(primary, Manifest{ID: "b3b3b3b3", SandboxOf: "d5d5d5d5", Wall: member.Wall}); err == nil {
		t.Fatal("foreign member data was accepted as a template")
	}
	source := &ForkSourceV1{selected: member.ID, operation: &SessionOperation{}}
	if _, err := source.memberV1(context.Background()); err == nil {
		t.Fatal("member data minted its original root receipt")
	}
}

func TestForkDerivedNameKeepsValidMaximumLength(t *testing.T) {
	base := strings.Repeat("界", api.SessionNameMax)
	name := forkDerivedNameV1(forkDerivedNameV1(base, "-fork"), "-32")
	if _, _, ok := api.CheckName(name); !ok {
		t.Fatal("fork of a valid maximum-length name became invalid")
	}
	if !strings.HasSuffix(name, "-32") {
		t.Fatal("fork child index was lost")
	}
}
