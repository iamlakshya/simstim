package guest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/memberenv"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

const secretStoreTimeout = 30 * time.Second

type secretStorePlan struct {
	name     string
	argv     []string
	sentinel api.Secret
}

func (secretStorePlan) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

// baseEnv is the owned member environment BEFORE any secret additions.
// Callers reserve memberStarts across this operation and the actual spawn.
// Only the registry's bound sentinel projection may enter sentinels.
func (g *Init) storeBoundSecrets(ctx context.Context, sessionID, agentKind, cwd string, baseEnv []string, sentinels map[string]api.Secret) error {
	if ctx == nil {
		return secretStoreError("")
	}
	ctx, cancel := phaseclock.WithDeadline(ctx, phaseclock.Current.Now().Add(secretStoreTimeout))
	defer cancel()
	plans, env, err := prepareSecretStores(sessionID, agentKind, cwd, baseEnv, sentinels)
	if err != nil {
		return err
	}
	output := &secretStoreDiscard{remaining: maxSecretStoreOutput, cancel: cancel}
	for _, plan := range plans {
		if g == nil || ctx.Err() != nil {
			return secretStoreError(plan.name)
		}
		if err := g.runSecretStore(ctx, sessionID, cwd, env, plan, output); err != nil {
			return err
		}
	}
	return nil
}

func (g *Init) runSecretStore(ctx context.Context, sessionID, cwd string, env []string, plan secretStorePlan, output *secretStoreDiscard) error {
	run := newOwnedCommand(ctx, proto.CtlExecArgs{ID: sessionID, Argv: plan.argv, Cwd: cwd}, env)
	run.purpose = "secret-store"
	defer run.cancel()
	code := g.executeOwned(run, ownedCommandIO{
		Input: strings.NewReader(string(plan.sentinel)), Out: output, Err: output,
	})
	if code != 0 || ctx.Err() != nil || output.exceeded() {
		return secretStoreError(plan.name)
	}
	return nil
}

// These wrappers retain the guest's private representation; all planning and
// filtering runs through the same pure implementation used by the supervisor.
func prepareSecretStores(sessionID, agentKind, cwd string, baseEnv []string, sentinels map[string]api.Secret) ([]secretStorePlan, []string, error) {
	plans, env, err := memberenv.PrepareStores(sessionID, agentKind, cwd, baseEnv, sentinels)
	return guestStorePlans(plans), env, err
}
func planSecretStores(rows []agents.SecretBinding, sentinels map[string]api.Secret) ([]secretStorePlan, map[string]bool, error) {
	plans, credentials, err := memberenv.PlanStores(rows, sentinels)
	return guestStorePlans(plans), credentials, err
}
func guestStorePlans(plans []memberenv.StorePlan) []secretStorePlan {
	result := make([]secretStorePlan, len(plans))
	for i, p := range plans {
		result[i] = secretStorePlan{name: p.Name, argv: p.Argv, sentinel: p.Sentinel}
	}
	return result
}
func secretStoreEnvironment(env []string, sentinels map[string]api.Secret, credentials map[string]bool, name string) ([]string, error) {
	return memberenv.StoreEnvironment(env, sentinels, credentials, name)
}
func secretStoreMetadata(name string) bool { return memberenv.StoreMetadata(name) }
func secretStoreEnvName(name string) bool  { return memberenv.EnvName(name) }
func secretStoreError(name string) error   { return memberenv.StoreError(name) }
