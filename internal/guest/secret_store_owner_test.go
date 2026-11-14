package guest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func storeClaimFixture() secretStoreClaim {
	return secretStoreClaim{argv: []string{"tool", "login"}, cwd: "/work", env: []string{"HOME=/home/agent", "PATH=/usr/bin:/bin"}, sentinel: "public-sentinel"}
}

func TestGuestStoreInstalledSameInvocationSkipsCommandEntirely(t *testing.T) {
	owner := newSecretStoreOwnership()
	claim := storeClaimFixture()
	calls := 0
	run := func() error { calls++; return nil }
	if err := owner.install(context.Background(), "KEY", claim, run); err != nil {
		t.Fatal(err)
	}
	if err := owner.install(context.Background(), "KEY", claim, run); err != nil || calls != 1 {
		t.Fatal("same successful Store command was executed again")
	}
}

func TestGuestStoreRejectsChangedSentinelCommandAndExactContextBeforeRun(t *testing.T) {
	for _, change := range []func(*secretStoreClaim){
		func(c *secretStoreClaim) { c.sentinel = "different-public-sentinel" },
		func(c *secretStoreClaim) { c.argv = []string{"other-tool", "login"} },
		func(c *secretStoreClaim) { c.cwd = "/other-work" },
		func(c *secretStoreClaim) { c.env = []string{"HOME=/other-home"} },
		func(c *secretStoreClaim) { c.env = []string{"CODEX_HOME=/different-state"} },
	} {
		owner := newSecretStoreOwnership()
		calls := 0
		if err := owner.install(context.Background(), "KEY", storeClaimFixture(), func() error { calls++; return nil }); err != nil {
			t.Fatal(err)
		}
		claim := storeClaimFixture()
		change(&claim)
		err := owner.install(context.Background(), "KEY", claim, func() error { calls++; return nil })
		requireSecretStoreError(t, err, "different-public-sentinel", "other-tool", "/other-work", "/other-home")
		if calls != 1 {
			t.Fatal("conflicting Store mutated an existing member login")
		}
		if !strings.Contains(err.(*proto.Error).Hint, "afresh") {
			t.Fatal("conflict recovery failed to require a fresh guest")
		}
	}
}

func TestGuestStoreFailedClaimCannotRetryOrRewrite(t *testing.T) {
	owner := newSecretStoreOwnership()
	calls := 0
	failure := secretStoreError("KEY")
	if err := owner.install(context.Background(), "KEY", storeClaimFixture(), func() error { calls++; return failure }); err != failure {
		t.Fatal("original Store failure changed")
	}
	err := owner.install(context.Background(), "KEY", storeClaimFixture(), func() error { calls++; return nil })
	requireSecretStoreError(t, err)
	if calls != 1 {
		t.Fatal("failed or partial auth write was retried in the same guest")
	}
}

func TestGuestStoreInstallationOwnsContextSlices(t *testing.T) {
	owner := newSecretStoreOwnership()
	claim := storeClaimFixture()
	if err := owner.install(context.Background(), "KEY", claim, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	claim.argv[0], claim.env[0] = "changed", "changed"
	calls := 0
	if err := owner.install(context.Background(), "KEY", storeClaimFixture(), func() error { calls++; return nil }); err != nil || calls != 0 {
		t.Fatal("caller metadata mutation changed the installed claim")
	}
}

func TestGuestStoreGateCancellationDoesNotRunAnotherCommand(t *testing.T) {
	owner := newSecretStoreOwnership()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	first := make(chan error, 1)
	go func() {
		first <- owner.install(context.Background(), "KEY", storeClaimFixture(), func() error { calls.Add(1); close(started); <-release; return nil })
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first Store did not reserve its claim")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := owner.install(ctx, "KEY", storeClaimFixture(), func() error { calls.Add(1); return nil })
	requireSecretStoreError(t, err)
	if calls.Load() != 1 {
		t.Fatal("cancelled waiting Store executed")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first Store did not release its ownership gate")
	}
	if err := owner.install(context.Background(), "KEY", storeClaimFixture(), func() error { calls.Add(1); return nil }); err != nil || calls.Load() != 1 {
		t.Fatal("successful first install was not idempotent after gate cancellation")
	}
}

func TestGuestStoreModesDoNotRunExposedAbsentOrCustomCommands(t *testing.T) {
	kind := storeAgentFixture(t, []agents.SecretBinding{{Env: "KEY", Store: []string{"unavailable-tool"}}})
	var g *Init
	for _, a := range []proto.CtlStartArgs{
		{Session: "store234", Agent: kind, Secrets: map[string]string{"KEY": "exposed-real"}, SecretModes: map[string]api.SecretMode{"KEY": api.SecretModeExposed}},
		{Session: "store234", Agent: kind, SecretModes: map[string]api.SecretMode{}},
		{Session: "store234", Agent: "custom-command", Secrets: map[string]string{"KEY": "public"}, SecretModes: map[string]api.SecretMode{"KEY": api.SecretModeBound}},
	} {
		if err := g.prepareMemberSecretStore(context.Background(), a); err != nil {
			t.Fatal("no selected Store unexpectedly required an executor")
		}
	}
}

func TestGuestStoreAmbiguousMultiRowTargetsRefuseBeforeExecutor(t *testing.T) {
	kind := storeAgentFixture(t, []agents.SecretBinding{{Env: "KEY_ONE", Store: []string{"tool", "first"}}, {Env: "KEY_TWO", Store: []string{"tool", "second"}}})
	g := &Init{memberStarts: map[string]bool{"store234": true}}
	args := proto.CtlStartArgs{Session: "store234", Agent: kind, Cwd: "/work", Env: []string{"HOME=/home/agent"}, Secrets: map[string]string{"KEY_ONE": "public-one", "KEY_TWO": "public-two"}, SecretModes: map[string]api.SecretMode{"KEY_ONE": api.SecretModeBound, "KEY_TWO": api.SecretModeBound}}
	err := g.prepareMemberSecretStore(context.Background(), args)
	requireSecretStoreError(t, err)
	if g.secretStores != nil || len(g.owned) != 0 {
		t.Fatal("ambiguous targets reserved or started a partial Store")
	}
}

func TestGuestStoreClaimFormattingDoesNotExposeContext(t *testing.T) {
	const marker = "private-store-context"
	claim := secretStoreClaim{argv: []string{marker}, env: []string{marker}, cwd: marker, sentinel: marker}
	owner := newSecretStoreOwnership()
	owner.state.claim = &claim
	for _, value := range []any{claim, &claim, owner, *owner} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
			if strings.Contains(fmt.Sprintf(verb, value), marker) {
				t.Fatal("Store ownership formatting leaked context")
			}
		}
	}
}

func TestGuestStoreStoppedOrUnreservedMemberCannotInstall(t *testing.T) {
	kind := storeAgentFixture(t, []agents.SecretBinding{{Env: "KEY", Store: []string{"unavailable-tool"}}})
	args := proto.CtlStartArgs{Session: "store234", Agent: kind, Cwd: "/work", Secrets: map[string]string{"KEY": "public"}, SecretModes: map[string]api.SecretMode{"KEY": api.SecretModeBound}}
	for _, g := range []*Init{{ended: true, memberStarts: map[string]bool{"store234": true}}, {memberStarts: map[string]bool{}}} {
		err := g.prepareMemberSecretStore(context.Background(), args)
		requireSecretStoreError(t, err)
		if g.secretStores != nil || len(g.owned) != 0 {
			t.Fatal("stopped/unreserved member created Store ownership")
		}
	}
	if err := (*Init)(nil).prepareMemberSecretStore(nil, args); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("nil Store lifetime did not fail safely")
	}
}
