package client

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

func (c *Conn) needsResumeMembers(ctx context.Context, id string) (bool, error) {
	r := grantRequestOf(ctx)
	if r != nil {
		r.mu.Lock()
		prepared := r.resumeGroup || r.resume != nil
		r.mu.Unlock()
		if prepared {
			return true, nil
		}
	}
	p, err := LoadSandboxPolicy(ctx, c.Host, id)
	if err != nil {
		return false, err
	}
	return p != nil && (p.FutureLineage || p.lineage != nil || p.CreationWall != "none" && (c.HasFeature(api.FeatureSpawn) || c.HasFeature(api.FeatureSandboxSnapshot))), nil
}

func (c *Conn) checkResumeBundle(ctx context.Context, a ResumeArgs) (Tier, error) {
	r := grantRequestOf(ctx)
	r.mu.Lock()
	before := cloneResumeGrantIntent(r.resume)
	r.mu.Unlock()
	names := secretMapNames(a.Secrets)
	if before != nil {
		names = before.originalNames
	}
	i, tier, err := c.prepareResumeMemberSelection(ctx, a.ID, names, actualResumeNames(a))
	if err != nil {
		return tier, err
	}
	if !sameGrantSnapshot(a.Snapshot, i.snapshot) || !slices.Equal(secretMapNames(a.Secrets), i.primaryNames) ||
		len(a.Members) != len(i.snapshot.Members) {
		return tier, joinIntentError()
	}
	for n, member := range a.Members {
		if member.ID != i.snapshot.Members[n] || !slices.Equal(secretMapNames(member.Secrets), i.members[member.ID].names) {
			return tier, joinIntentError()
		}
	}
	return tier, nil
}

func cloneResumeValues(a ResumeArgs) ResumeArgs {
	a.ProtectedTrustedSecretValues = maps.Clone(a.ProtectedTrustedSecretValues)
	if a.SecretDelivery != nil {
		intent := api.CloneSecretDeliveryIntent(*a.SecretDelivery)
		a.SecretDelivery = &intent
	}
	a.NamedSSHKeys = api.CloneNamedSSHKeysV1(a.NamedSSHKeys)
	a.Prepared = api.ClonePreparedResumeV1(a.Prepared)
	a.ExpectedImageV1 = slices.Clone(a.ExpectedImageV1)
	a.VMNeedV1 = slices.Clone(a.VMNeedV1)
	a.Secrets, a.Snapshot = maps.Clone(a.Secrets), cloneGrantSnapshot(a.Snapshot)
	a.Members = slices.Clone(a.Members)
	for n := range a.Members {
		a.Members[n].Secrets = maps.Clone(a.Members[n].Secrets)
		a.Members[n].ProtectedTrustedSecretValues = maps.Clone(a.Members[n].ProtectedTrustedSecretValues)
	}
	return a
}

type resumeActualNames struct {
	snapshot *api.SandboxSnapshot
	primary  []string
	ids      []string
	children map[string][]string
}

func actualResumeNames(a ResumeArgs) *resumeActualNames {
	selection := &resumeActualNames{snapshot: cloneGrantSnapshot(a.Snapshot), primary: secretMapNames(a.Secrets), children: map[string][]string{}}
	selection.ids = make([]string, 0, len(a.Members))
	for _, member := range a.Members {
		selection.ids = append(selection.ids, member.ID)
		selection.children[member.ID] = secretMapNames(member.Secrets)
	}
	return selection
}
