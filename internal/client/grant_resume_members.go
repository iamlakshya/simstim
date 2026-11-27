package client

import (
	"context"
	"slices"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
)

type resumeMemberGrant struct {
	creation *SandboxPolicy
	names    []string
}

type resumeGrantIntent struct {
	host, hostID  string
	connection    *Conn
	savedHost     Host
	original      string
	originalNames []string
	snapshot      *api.SandboxSnapshot
	primaryNames  []string
	members       map[string]resumeMemberGrant
}

// ResumeSecretTarget checks the whole group before any value is fetched.
// A known local lineage alone may select another member's primary values.
// The caller keeps ResumeArgs.ID equal to originalID.
func (c *Conn) ResumeSecretTarget(ctx context.Context, originalID string, names []string) (string, []string, error) {
	ctx, _ = ensureGrantRequest(ctx)
	i, _, err := c.prepareResumeMembers(ctx, originalID, names)
	if err != nil {
		return "", nil, err
	}
	return i.snapshot.Primary, slices.Clone(i.primaryNames), nil
}

// ResumeMemberSecrets fetches only locally recorded member names. Every
// member passes the current gate before the first child command is run.
func (c *Conn) ResumeMemberSecrets(ctx context.Context, originalID string, terminal bool) ([]api.MemberResumeArgs, error) {
	ctx, r := ensureGrantRequest(ctx)
	r.mu.Lock()
	before := cloneResumeGrantIntent(r.resume)
	r.mu.Unlock()
	if before == nil || before.original != originalID {
		return nil, joinIntentError()
	}
	i, _, err := c.prepareResumeMembers(ctx, originalID, before.originalNames)
	if err != nil {
		return nil, err
	}
	out := make([]api.MemberResumeArgs, 0, len(i.snapshot.Members))
	for _, id := range i.snapshot.Members {
		// A host/tier or local-record mutation after an earlier command must
		// stop the next read too. Final publication repeats the whole check.
		if _, _, err := c.prepareResumeMembers(ctx, originalID, before.originalNames); err != nil {
			return nil, err
		}
		member := api.MemberResumeArgs{ID: id}
		if names := i.members[id].names; len(names) != 0 {
			member.Secrets, err = FetchSessionSecrets(ctx, c.Host, id, names, terminal)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, member)
	}
	return out, nil
}

func (c *Conn) prepareResumeMembers(ctx context.Context, original string, originalNames []string) (*resumeGrantIntent, Tier, error) {
	return c.prepareResumeMemberSelection(ctx, original, originalNames, nil)
}

func (c *Conn) prepareResumeMemberSelection(ctx context.Context, original string, originalNames []string, actual *resumeActualNames) (*resumeGrantIntent, Tier, error) {
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return nil, TierUnknown, err
	}
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return nil, TierUnknown, err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	r.resumeGroup = true
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, TierUnknown, err
	}
	if !c.HasFeature(api.FeatureSpawn) || !c.HasFeature(api.FeatureSandboxSnapshot) || !c.HasFeature("resume") {
		return nil, TierUnknown, c.grantHelperOld("resume a complete shared sandbox")
	}
	if err := checkSessionID(c.Host.Name, original); err != nil {
		return nil, TierUnknown, err
	}
	if r.SSHAgent || requestWallIntent(ctx) != nil {
		return nil, TierUnknown, joinIntentError()
	}
	_, selected, err := EffectiveSessionTier(ctx, c.Host, original, c.HostID())
	if err != nil {
		return nil, TierUnknown, err
	}
	if err := checkLocalSandboxNames(selected, originalNames); err != nil {
		return nil, TierUnknown, err
	}
	r.mu.Lock()
	before := cloneResumeGrantIntent(r.resume)
	legacy := r.legacyResume != nil
	r.mu.Unlock()
	if legacy {
		return nil, TierUnknown, grantOperationError()
	}
	var snapshot *api.SandboxSnapshot
	if before != nil {
		snapshot = cloneGrantSnapshot(before.snapshot)
	} else {
		snapshot, err = c.SandboxSnapshot(ctx, original)
		if err != nil {
			return nil, TierUnknown, err
		}
	}
	if err := checkSnapshotAuthority(snapshot, original, selected); err != nil {
		return nil, TierUnknown, err
	}
	if actual != nil && (!slices.Equal(actual.ids, snapshot.Members) ||
		actual.snapshot != nil && !sameGrantSnapshot(actual.snapshot, snapshot)) {
		return nil, TierUnknown, joinIntentError()
	}
	i := &resumeGrantIntent{host: c.Host.Name, hostID: c.HostID(), connection: c, savedHost: cloneGrantHost(c.Host), original: original,
		originalNames: slices.Clone(originalNames), snapshot: snapshot, members: map[string]resumeMemberGrant{}}
	sort.Strings(i.originalNames)
	if len(slices.Compact(slices.Clone(i.originalNames))) != len(i.originalNames) {
		return nil, TierUnknown, joinIntentError()
	}
	settings := LoadSettings()
	var bundleTier = TierOwn
	tiers := map[string]Tier{}
	for _, id := range append([]string{snapshot.Primary}, snapshot.Members...) {
		tier, p, err := EffectiveSessionTier(ctx, c.Host, id, c.HostID())
		if err != nil {
			return nil, tier, err
		}
		if p != nil && (p.HostID == "" || p.HostID != c.HostID() ||
			p.SSHAgent || !p.FutureLineage && localSandboxRoot(p) != snapshot.Primary) {
			return nil, tier, joinIntentError()
		}
		var names []string
		if prior, ok := resumePriorMember(before, id); ok {
			names = slices.Clone(prior.names)
		} else if actual != nil {
			if id == snapshot.Primary {
				names = slices.Clone(actual.primary)
			} else {
				names = slices.Clone(actual.children[id])
			}
		} else if selected != nil && !selected.FutureLineage && p != nil && !p.FutureLineage {
			if id == snapshot.Primary && original == id {
				names = slices.Clone(i.originalNames)
			} else {
				names = slices.Clone(p.Secrets)
				sort.Strings(names)
			}
		}
		// A structural answer never lets an unknown selected member borrow
		// another local member's authority, including in direct SDK bundles.
		if (selected == nil || selected.FutureLineage) && len(names) != 0 {
			return nil, tier, joinIntentError()
		}
		if selected != nil && !selected.FutureLineage && id == snapshot.Primary && p == nil {
			return nil, tier, joinIntentError()
		}
		if p != nil && p.CreationWall == "none" && len(names) != 0 {
			return nil, tier, joinIntentError()
		}
		if err := checkLocalSandboxNames(p, names); err != nil {
			return nil, tier, err
		}
		if err := c.requireSecretFeature(len(names) != 0); err != nil {
			return nil, tier, err
		}
		if prior, ok := resumePriorMember(before, id); ok && prior.creation != nil {
			tier = min(tier, prior.creation.Ceiling)
		}
		tiers[id] = tier
		i.members[id] = resumeMemberGrant{creation: cloneSandboxPolicy(p), names: slices.Clone(names)}
		bundleTier = min(bundleTier, tier)
		if id == snapshot.Primary {
			i.primaryNames = slices.Clone(names)
		}
	}
	if err := r.bindResumeMembers(i); err != nil {
		return nil, bundleTier, err
	}
	// Pin all local creation facts before the first question. A retry may
	// neither adopt a newly appeared record nor enlarge a selected set.
	for _, id := range append([]string{snapshot.Primary}, snapshot.Members...) {
		member := i.members[id]
		if err := allowResumeSecretNames(ctx, c.Host, c.HostID(), tiers[id], settings, member.names, member.creation); err != nil {
			return nil, tiers[id], err
		}
	}
	return i, bundleTier, nil
}

func resumePriorMember(i *resumeGrantIntent, id string) (resumeMemberGrant, bool) {
	if i == nil {
		return resumeMemberGrant{}, false
	}
	m, ok := i.members[id]
	return m, ok
}

func (r *GrantRequest) bindResumeMembers(i *resumeGrantIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if before := r.resume; before != nil {
		if !SameEntry(before.savedHost, i.savedHost) || before.host != i.host || before.hostID != i.hostID || before.original != i.original ||
			!sameGrantSnapshot(before.snapshot, i.snapshot) || !slices.Equal(before.originalNames, i.originalNames) {
			return joinIntentError()
		}
		for id, member := range i.members {
			prior, ok := before.members[id]
			if !ok || !sameSandboxCreation(prior.creation, member.creation) || !slices.Equal(prior.names, member.names) {
				return joinIntentError()
			}
		}
		before.connection = i.connection
		return nil
	}
	r.resume = cloneResumeGrantIntent(i)
	return nil
}

func cloneResumeGrantIntent(i *resumeGrantIntent) *resumeGrantIntent {
	if i == nil {
		return nil
	}
	q := *i
	q.savedHost = cloneGrantHost(i.savedHost)
	q.originalNames, q.primaryNames = slices.Clone(i.originalNames), slices.Clone(i.primaryNames)
	q.snapshot, q.members = cloneGrantSnapshot(i.snapshot), map[string]resumeMemberGrant{}
	for id, m := range i.members {
		q.members[id] = resumeMemberGrant{creation: cloneSandboxPolicy(m.creation), names: slices.Clone(m.names)}
	}
	return &q
}
