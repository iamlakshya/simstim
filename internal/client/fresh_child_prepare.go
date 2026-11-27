package client

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func (c *Conn) newFreshChildPlan(ctx context.Context, request *GrantRequest, operation *settingsOperation, choice api.JoinArgs) (*freshChildPlan, error) {
	settings, err := operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	metadata, err := newOrdinaryLocalGitMetadataSource(ctx, operation)
	if err != nil {
		return nil, err
	}
	keys := append([]string{}, gitMetadataCheckKeys(localWindows())...)
	keys = append(keys, "TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION")
	check := metadata.CheckKeys(ctx, keys)
	if err := errors.Join(check, metadata.Close()); err != nil {
		return nil, err
	}
	layers, err := ResolvePolicyLayers(settings, c.Host, "", "", api.LayerValues{})
	if err != nil {
		return nil, err
	}
	selection, err := selectNewAgentSecrets(settings, c.Host, NewArgs{Agent: choice.Agent, Argv: slices.Clone(choice.Argv)}, layers)
	if err != nil {
		return nil, err
	}
	if _, known := agents.Find(selection.Kind()); !known {
		return nil, &UserError{Msg: "a fresh member needs a canonical local agent kind", Hint: "Choose a listed --agent; use --agent shell with an explicit command.", Code: "bad_agent", Kind: KindUsage}
	}
	if _, hostID, err := grantHostTier(ctx, c.Host); err != nil {
		return nil, err
	} else if hostID != c.HostID() {
		return nil, grantIdentityError(c.Host)
	}
	ancestors, err := captureFreshSandboxReservationContext(ctx, c, choice.Parent)
	if err != nil {
		return nil, err
	}
	if ancestors.parent.futureDeliveryReceipt || ancestors.root.futureDeliveryReceipt || ancestors.parent.futureInputOperation || ancestors.root.futureInputOperation {
		return nil, futureSandboxReceiptError()
	}
	if ancestors.parent.inputOperation != nil || ancestors.root.inputOperation != nil {
		return nil, &UserError{Msg: "this sandbox still has a local input operation to finish", Hint: "Finish the original input operation before adding a member.", Code: "input_pending", Kind: KindRefused}
	}
	wall, err := makeWallGrantIntent(false, ancestors.root.CreationWall, ancestors.root.Exposed)
	if err != nil {
		return nil, err
	}
	intent, err := freshChildDelivery(settings, selection, wall)
	if err != nil {
		return nil, err
	}
	cohort, err := c.readFreshChildCohort(ctx, choice.Parent)
	if err != nil {
		return nil, err
	}
	if cohort.Snapshot.Primary != ancestors.root.SessionID {
		return nil, joinIntentError()
	}
	if err := checkFreshCohortDelivery(ctx, cohort.SecretState.Intent, intent); err != nil {
		return nil, err
	}
	term := terminalArgs(NewArgs{}, choice.Cols, choice.Rows)
	choice.Argv = slices.Clone(choice.Argv)
	choice.Secrets = nil
	header := choice
	header.Argv = slices.Clone(choice.Argv)
	header.Term, header.ColorTerm = term.Term, term.ColorTerm
	header.TermProgram, header.TermProgramVersion = term.TermProgram, term.TermProgramVersion
	if err := operation.Revalidate(ctx); err != nil {
		return nil, err
	}
	return &freshChildPlan{request: request, conn: c, operation: operation, choice: choice, header: header, selection: selection,
		ancestors: ancestors, cohort: cohort, wall: wall, intent: intent}, nil
}

func freshChildDelivery(settings Settings, selection *LocalAgentSelection, wall *wallGrantIntent) (api.SecretDeliveryIntent, error) {
	intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}
	defaults := map[string]api.SecretBind{}
	agent, _ := agents.Find(selection.Kind())
	for _, binding := range agent.SecretBindings() {
		candidate := api.SecretBind{Hosts: slices.Clone(binding.Hosts), Header: binding.Header, Format: binding.Format}
		if prior, exists := defaults[binding.Env]; exists && !reflect.DeepEqual(prior, candidate) {
			return intent, grantOperationError()
		}
		defaults[binding.Env] = candidate
	}
	definitions := make(map[string]SecretDefinition, len(selection.names))
	for _, name := range selection.names {
		definitions[name] = settings.Secrets[name]
	}
	view, err := NormaliseSecretDefinitions(definitions, defaults)
	if err != nil {
		return intent, err
	}
	for _, name := range selection.names {
		if wall.exposed[name] {
			intent.Modes[name] = api.SecretModeExposed
			continue
		}
		binding, bound := view.Binds[name]
		if !bound || wall.level == "process" {
			return intent, &UserError{Msg: "the fresh member's local secret has no safe delivery binding", Hint: "Configure its HTTPS binding, or start a new sandbox with its explicit exposure.", Code: "secret_unbound", Kind: KindRefused}
		}
		intent.Modes[name], intent.Binds[name] = api.SecretModeBound, binding
	}
	return intent, nil
}

func (c *Conn) readFreshChildCohort(ctx context.Context, parent string) (*api.SandboxCohortView, error) {
	if !c.HasFeature(api.FeatureSpawn) || !c.HasFeature(api.FeatureSandboxSnapshot) || !c.HasFeature(api.FeatureSandboxCohort) || !c.HasFeature(api.FeatureBoundSecrets) {
		return nil, c.grantHelperOld("seal a fresh local member and current sandbox cohort")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "sandbox.snapshot", api.SandboxSnapshotArgs{ID: parent, Cohort: true}, &raw); err != nil {
		return nil, err
	}
	view, err := api.DecodeSandboxCohort(raw, parent)
	if err != nil {
		return nil, joinIntentError()
	}
	if err := egress.ValidateSavedPublicSessionState(view.SecretState); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	return view, nil
}
