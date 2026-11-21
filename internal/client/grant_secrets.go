package client

import (
	"context"
	"errors"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
)

// FetchHostSecrets checks every grant before any secret command runs.
// A shared-host question returns to the caller, which owns terminal input.
func FetchHostSecrets(ctx context.Context, h Host, names []string, terminal bool) (_ map[string]api.Secret, returned error) {
	standalone := grantRequestOf(ctx) == nil
	ctx, request := ensureGrantRequest(ctx)
	if standalone {
		defer func() { returned = errors.Join(returned, request.Close()) }()
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return nil, err
	}
	if err := checkRequestedGrantWall(ctx); err != nil {
		return nil, err
	}
	if err := checkWallAgentIntent(ctx, newFetchAgentIntent(request)); err != nil {
		return nil, err
	}
	if _, err := SandboxPolicies(ctx, h); err != nil {
		return nil, err
	}
	tier, hostID, err := grantHostTier(ctx, h)
	if err != nil {
		return nil, err
	}
	tier = min(tier, requestCeiling(ctx, tier))
	if err := bindRequestLocalSource(ctx, h); err != nil {
		return nil, err
	}
	if err := bindNewGrantIntent(ctx, h, hostID, names, newFetchAgentIntent(grantRequestOf(ctx))); err != nil {
		return nil, err
	}
	settings, err := newOperationSettings(ctx)
	if err != nil {
		return nil, err
	}
	if newFetchAgentIntent(request) {
		if err := allowRequestGrant(ctx, h, hostID, tier, Outbound{Kind: OutboundSSHAgent}, settings); err != nil {
			return nil, err
		}
		if err := CheckLocalSSHAgentContext(ctx); err != nil {
			return nil, err
		}
	}
	if err := allowSecretNames(ctx, h, hostID, tier, settings, names); err != nil {
		return nil, err
	}
	if plan := newPlanOf(ctx); plan != nil {
		return plan.fetch(ctx, h, names, terminal)
	}
	if inputs := newInputPreparationOf(ctx); inputs != nil {
		return inputs.fetchUnwalled(ctx, h, names, terminal)
	}
	return FetchSecrets(ctx, settings, names, terminal)
}

// FetchSessionSecrets checks the local request ceiling again before a
// resume fetches any values. A host's own session list cannot raise it.
func FetchSessionSecrets(ctx context.Context, h Host, id string, names []string, terminal bool) (map[string]api.Secret, error) {
	ctx, _ = ensureGrantRequest(ctx)
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return nil, err
	}
	if err := bindRequestLocalSource(ctx, h); err != nil {
		return nil, err
	}
	if err := checkSharedSecretFetch(ctx, h, id, names); err != nil {
		return nil, err
	}
	_, hostID, err := grantHostTier(ctx, h)
	if err != nil {
		return nil, err
	}
	tier, policy, err := EffectiveSessionTier(ctx, h, id, hostID)
	if err != nil {
		return nil, err
	}
	if err := checkDerivedSecretFetch(ctx, policy); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	shared := r.join != nil || r.resume != nil
	r.mu.Unlock()
	if !shared {
		if err := bindLegacyResumeIntent(ctx, h, hostID, id, names, policy); err != nil {
			return nil, err
		}
	}

	ctx, imageOperation, err := resumeImageBeforeValues(ctx, h, id, policy)
	if err != nil {
		return nil, err
	}
	var settings Settings
	if imageOperation != nil {
		settings, err = imageOperation.Settings(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		settings = LoadSettings()
	}
	if policy != nil && policy.SSHAgent {
		if err := allowRequestGrant(ctx, h, hostID, tier, Outbound{Kind: OutboundSSHAgent}, settings); err != nil {
			return nil, err
		}
	}
	if err := allowResumeSecretNames(ctx, h, hostID, tier, settings, names, policy); err != nil {
		return nil, err
	}
	if rewind := originalRewindV1(ctx); rewind != nil {
		if err := rewind.checkMemberValuesV1(ctx, h, id, names); err != nil {
			return nil, err
		}
		values, err := FetchSecrets(ctx, settings, names, terminal)
		if err != nil {
			return nil, err
		}
		if err := rewind.checkMemberValuesV1(ctx, h, id, names); err != nil {
			clear(values)
			return nil, err
		}
		return values, nil
	}
	resumeConn, err := originalResumeValueConnectionV1(ctx, h, id)
	if err != nil {
		return nil, err
	}
	if resumeConn != nil {
		if err := prepareTrustedJudgeResumeV1(ctx, resumeConn, id); err != nil {
			return nil, err
		}
		if _, err := checkTrustedJudgeResumeBeforeValuesV1(ctx, resumeConn, id); err != nil {
			return nil, err
		}
	}
	values, err := FetchSecrets(ctx, settings, names, terminal)
	if err != nil {
		return nil, err
	}
	if resumeConn != nil {
		if err := fetchTrustedJudgeResumeV1(ctx, resumeConn, id, values, terminal); err != nil {
			return nil, err
		}
	}
	return values, nil
}

// VisibleSecretNames lists names eligible for the new-session form.
// Confirmed is used only to distinguish eligibility from a pending live
// question. It neither saves an approval nor fetches any value.
func VisibleSecretNames(h Host, tier Tier, settings Settings, names []string) ([]string, error) {
	if err := ValidateSettings(settings); err != nil {
		return nil, err
	}
	var out []string
	for _, name := range SecretNames(Host{Secrets: names}, nil) {
		err := Allow(h, tier, Outbound{Kind: OutboundSecret, Name: name, Confirmed: true}, settings)
		if err == nil {
			out = append(out, name)
			continue
		}
		var refused *UserError
		if errors.As(err, &refused) && refused.Kind == KindRefused {
			continue
		}
		return nil, err
	}
	return out, nil
}

func allowSecretNames(ctx context.Context, h Host, hostID string, tier Tier, settings Settings, names []string) error {
	return allowSecretNamesAtWall(ctx, h, hostID, tier, settings, names, requestWallIntent(ctx), true)
}

func allowSecretNamesAtWall(ctx context.Context, h Host, hostID string, tier Tier, settings Settings, names []string, intent *wallGrantIntent, creationKnown bool) error {
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	for _, name := range names {
		if err := checkSessionMachineSecretV1(h, settings, name, intent); err != nil {
			return err
		}
		if err := allowRequestGrant(ctx, h, hostID, tier, Outbound{Kind: OutboundSecret, Name: name}, settings); err != nil {
			return err
		}
		minimum, err := minimumSecretTier(settings, name)
		if err != nil {
			return err
		}
		if !creationKnown && minimum == TierOwn {
			return missingResumeIntent(name)
		}
		if err := allowSecretAtWall(intent, name, minimum); err != nil {
			return err
		}
	}
	return nil
}

func secretMapNames(values map[string]api.Secret) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func recordSecretNames(ctx context.Context, h Host, hostID string, tier Tier, values map[string]api.Secret) error {
	for _, name := range secretMapNames(values) {
		if err := recordRequestGrant(ctx, h, hostID, tier, Outbound{Kind: OutboundSecret, Name: name}); err != nil {
			return err
		}
	}
	return nil
}
