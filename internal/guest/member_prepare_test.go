package guest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

func preparedFixture(t *testing.T) (*Init, proto.CtlStartArgs) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g := &Init{lifetime: ctx, cancelLifetime: cancel, sessionPrepared: true, primary: "parent234", members: map[string]*holderProc{"parent234": {id: "parent234", done: make(chan struct{})}}, memberStarts: map[string]bool{}}
	a := proto.CtlStartArgs{Session: "child234", RunToken: "current/child234", Argv: []string{"/bin/true"}, Env: []string{"HOME=/home/agent", "PATH=/usr/bin:/bin"}, Cwd: "/work", SecretModes: map[string]api.SecretMode{}}
	return g, a
}
func prepareFixtureMember(t *testing.T, g *Init, a proto.CtlStartArgs) proto.CtlStartArgs {
	t.Helper()
	raw, err := json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlSpawn, Start: a})
	if err != nil {
		t.Fatal(err)
	}
	result, err := g.prepareMember(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.PreparedClaim = result.Claim
	if g.commandHolds != 0 || len(g.members) != 1 || g.holder != nil {
		t.Fatal("preparation launched a holder or leaked an execution hold")
	}
	return a
}
func TestMemberPreparedClaimBindsRoleRawIntentAndConsumesOnce(t *testing.T) {
	changes := []struct {
		name   string
		change func(*proto.CtlStartArgs)
	}{
		{"run", func(a *proto.CtlStartArgs) { a.RunToken = "other/child234" }},
		{"session", func(a *proto.CtlStartArgs) { a.Session = "other234" }},
		{"argv", func(a *proto.CtlStartArgs) { a.Argv = []string{"/bin/false"} }},
		{"environment", func(a *proto.CtlStartArgs) { a.Env = append(a.Env, "USER_DATA=changed") }},
		{"cwd", func(a *proto.CtlStartArgs) { a.Cwd = "/other" }},
		{"mode-presence", func(a *proto.CtlStartArgs) { a.SecretModes = nil }},
		{"input", func(a *proto.CtlStartArgs) {
			a.InputNonce = strings.Repeat("a", 64)
			a.InputDigest = strings.Repeat("b", 64)
		}},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			g, a := preparedFixture(t)
			a = prepareFixtureMember(t, g, a)
			changed := cloneMemberArgs(a)
			change.change(&changed)
			if _, err := g.consumePreparedMember(changed, false); err == nil {
				t.Fatal("changed intent consumed claim")
			}
			if _, err := g.consumePreparedMember(a, true); err == nil {
				t.Fatal("Start consumed Spawn claim")
			}
			if _, err := g.consumePreparedMember(a, false); err != nil {
				t.Fatal(err)
			}
			if _, err := g.consumePreparedMember(a, false); err == nil {
				t.Fatal("consumed claim replayed")
			}
		})
	}
}
func TestMemberPreparationLostAckCannotRepeatOrInstallUnneededStore(t *testing.T) {
	g, a := preparedFixture(t)
	raw, _ := json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlSpawn, Start: a})
	result, err := g.prepareMember(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.prepareMember(raw); err == nil {
		t.Fatal("lost preparation ACK permitted another reservation")
	}
	a.PreparedClaim = result.Claim
	raw, _ = json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlSpawn, Start: a})
	if err := g.storePreparedMember(raw); err == nil {
		t.Fatal("unneeded Store accepted")
	}
	if g.commandHolds != 0 || g.secretStores != nil {
		t.Fatal("rejected Store mutated installation ownership")
	}
	g.members[g.primary].agentEnded = true
	if g.emptyLocked() {
		t.Fatal("idle prepared reservation did not protect its final launch")
	}
	cleanup, err := g.abortPreparedMember(raw)
	if err != nil || cleanup.State != "aborted" || !g.emptyLocked() {
		t.Fatal("exact abandoned reservation cleanup did not release an empty sandbox", err)
	}
}
func TestMemberPreparationCannotBypassInputAndClaimFieldsStayClosed(t *testing.T) {
	g, a := preparedFixture(t)
	a.InputNonce = strings.Repeat("a", 64)
	raw, _ := json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlSpawn, Start: a})
	if _, err := g.prepareMember(raw); err == nil {
		t.Fatal("foreign input identity reserved a member")
	}
	if len(g.preparedMembers) != 0 || g.commandHolds != 0 {
		t.Fatal("invalid input mutated preparation")
	}
	_, err := decodeInputPrepare([]byte(`{"control":{"session":"child234","nonce":"` + strings.Repeat("a", 64) + `","digest":"` + strings.Repeat("b", 64) + `","copy_max":67108864,"seed_max":536870912},"start":{"session":"child234","prepared_claim":null}}`))
	if err == nil {
		t.Fatal("input decoder accepted a member claim field")
	}
	if _, err := decodeControllerPrepare([]byte(`{"version":1,"start":{"session":"child234","pending_members":1,"prepared_claim":null}}`)); err == nil {
		t.Fatal("controller decoder accepted a member claim field")
	}
}
func TestInputPreparedPrimaryKeepsActualAgentCAAndNoEarlyHolder(t *testing.T) {
	g, a := preparedFixture(t)
	a.Session = "primary234"
	a.RunToken = "current/primary234"
	a.Agent = "codex"
	a.InputNonce = strings.Repeat("a", 64)
	a.InputDigest = strings.Repeat("b", 64)
	original := inputInfrastructure(a)
	g.inputStart = &original
	g.inputEnv = append([]string(nil), a.Env...)
	g.caReady = true
	g.members = nil
	g.primary = a.Session
	done := make(chan struct{})
	close(done)
	g.inputs = &inputPhase{control: inputtransfer.Control{Scope: inputtransfer.Scope{Session: a.Session, Nonce: api.InputNonce(a.InputNonce), Digest: a.InputDigest}}, success: true, done: done}
	raw, _ := json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlStart, Start: a})
	result, err := g.prepareMember(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !g.bootAdmission || g.holder != nil || g.commandHolds != 0 {
		t.Fatal("primary preparation ended boot admission or launched a holder")
	}
	a.PreparedClaim = result.Claim
	projected, err := g.consumePreparedMember(a, true)
	if err != nil {
		t.Fatal(err)
	}
	if envValue(projected.Env, "CODEX_CA_CERTIFICATE") == "" {
		t.Fatal("input preparation lost actual agent CA variable")
	}
}

func prepareBoundFixture(t *testing.T) (*Init, proto.CtlStartArgs) {
	t.Helper()
	g, a := preparedFixture(t)
	a.Agent = "codex"
	a.Secrets = map[string]string{"OPENAI_API_KEY": "public-sentinel"}
	a.SecretModes = map[string]api.SecretMode{"OPENAI_API_KEY": api.SecretModeBound}
	return g, prepareFixtureMember(t, g, a)
}
func TestMemberStoreLostAckCannotRepeatCompletedSuboperation(t *testing.T) {
	g, a := prepareBoundFixture(t)
	projected := g.preparedMembers[a.Session].projected
	bound, err := memberBoundSentinels(projected)
	if err != nil {
		t.Fatal(err)
	}
	plans, env, err := prepareSecretStores(a.Session, a.Agent, a.Cwd, projected.Env, bound)
	if err != nil || len(plans) != 1 {
		t.Fatal("canonical Store plan unavailable", err)
	}
	// Explicit state-machine fixture: an earlier identical batch installed once.
	// The production owner must deduplicate without executing any process here.
	g.secretStores = newSecretStoreOwnership()
	claim := g.secretStoreInvocation(plans[0], a.Cwd, env)
	if err := g.secretStores.install(g.lifetime, plans[0].name, claim, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlSpawn, Start: a})
	if err := g.storePreparedMember(raw); err != nil {
		t.Fatal(err)
	}
	if err := g.storePreparedMember(raw); err == nil {
		t.Fatal("lost Store ACK permitted sub-op replay")
	}
	if g.commandHolds != 0 || len(g.owned) != 0 {
		t.Fatal("deduplicated Store executed a child or leaked a hold")
	}
	if _, err := g.consumePreparedMember(a, false); err != nil {
		t.Fatal(err)
	}
	if _, err := g.consumePreparedMember(a, false); err == nil {
		t.Fatal("lost Store ACK permitted final claim replay")
	}
}
func TestMemberStoreLifetimeCancellationJoinsOccupiedFailure(t *testing.T) {
	g, a := prepareBoundFixture(t)
	g.secretStores = newSecretStoreOwnership()
	g.secretStores.state.gate <- struct{}{}
	defer func() { <-g.secretStores.state.gate }()
	raw, _ := json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlSpawn, Start: a})
	result := make(chan error, 1)
	go func() { result <- g.storePreparedMember(raw) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		g.mu.Lock()
		running := g.preparedMembers[a.Session].state == memberStoreRunning
		g.mu.Unlock()
		if running {
			break
		}
		select {
		case err := <-result:
			t.Fatal("Store ended before its blocked installation gate", err)
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("Store did not enter its owned sub-operation")
		}
	}
	g.cancelLifetime()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled Store succeeded")
		}
	case <-timeout.C:
		t.Fatal("cancelled Store did not join")
	}
	if g.ended || g.members[g.primary].agentEnded || g.commandHolds != 0 || g.memberStarts[a.Session] || g.preparedMembers[a.Session].state != memberFailed {
		t.Fatal("cancelled Store directly stopped another member or lost occupied failure state")
	}
	if err := g.storePreparedMember(raw); err == nil {
		t.Fatal("cancelled Store claim could replay")
	}
}
