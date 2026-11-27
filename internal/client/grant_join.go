package client

import (
	"context"
	"slices"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
)

type joinGrantIntent struct {
	host, hostID string
	connection   *Conn
	savedHost    Host
	parent       string
	creation     *SandboxPolicy
	snapshot     *api.SandboxSnapshot
	names        []string
	selected     bool
}

// JoinSecretNames reads the original local allowance. Neither current host
// configuration nor a remote session row can add a name to this operation.
func (c *Conn) JoinSecretNames(ctx context.Context, parentID string) ([]string, error) {
	ctx, _ = ensureGrantRequest(ctx)
	intent, _, err := c.joinGrantIntent(ctx, parentID, nil, false)
	if err != nil {
		return nil, err
	}
	return localSandboxNames(intent.creation), nil
}

// CheckJoinGrants must finish before any command fetches a Join value.
// JoinSession repeats it immediately before publishing its single frame.
func (c *Conn) CheckJoinGrants(ctx context.Context, parentID string, names []string) error {
	ctx, _ = ensureGrantRequest(ctx)
	_, _, err := c.joinGrantIntent(ctx, parentID, names, true)
	return err
}

func (c *Conn) joinGrantIntent(ctx context.Context, parentID string, names []string, selectNames bool) (*joinGrantIntent, Tier, error) {
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return nil, TierUnknown, err
	}
	if err := requireGrantOperation(ctx, grantOpJoin); err != nil {
		return nil, TierUnknown, err
	}
	if err := ctx.Err(); err != nil {
		return nil, TierUnknown, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, TierUnknown, err
	}
	if !c.HasFeature(api.FeatureSpawn) || !c.HasFeature(api.FeatureSandboxSnapshot) {
		return nil, TierUnknown, c.grantHelperOld("join a shared sandbox")
	}
	if err := checkSessionID(c.Host.Name, parentID); err != nil {
		return nil, TierUnknown, err
	}
	if err := c.requireSecretFeature(len(names) != 0); err != nil {
		return nil, TierUnknown, err
	}
	r := grantRequestOf(ctx)
	if r.SSHAgent || r.Untrusted || requestWallIntent(ctx) != nil {
		return nil, TierUnknown, joinIntentError()
	}
	tier, p, err := EffectiveSessionTier(ctx, c.Host, parentID, c.HostID())
	if err != nil {
		return nil, tier, err
	}
	if p != nil && p.FutureLineage {
		return nil, tier, futureLineageError()
	}
	if p != nil && (p.HostID == "" || p.HostID != c.HostID() || p.CreationWall == "none") {
		return nil, tier, joinIntentError()
	}
	if err := checkLocalSandboxNames(p, names); err != nil {
		return nil, tier, err
	}
	canonical := slices.Clone(names)
	sort.Strings(canonical)
	if len(slices.Compact(slices.Clone(canonical))) != len(canonical) {
		return nil, tier, joinIntentError()
	}
	r.mu.Lock()
	before := cloneJoinGrantIntent(r.join)
	r.mu.Unlock()
	var snapshot *api.SandboxSnapshot
	if before == nil {
		snapshot, err = c.SandboxSnapshot(ctx, parentID)
		if err != nil {
			return nil, tier, err
		}
	} else {
		snapshot = cloneGrantSnapshot(before.snapshot)
	}
	if err := checkSnapshotAuthority(snapshot, parentID, p); err != nil {
		return nil, tier, err
	}
	intent, err := r.bindJoinIntent(c, c.Host.Name, c.HostID(), parentID, p, snapshot, canonical, selectNames)
	if err != nil {
		return nil, tier, err
	}
	if intent.creation != nil {
		tier = min(tier, intent.creation.Ceiling)
	}
	// Validate the entire settings file even for a zero-value operation.
	settings := LoadSettings()
	if err := allowResumeSecretNames(ctx, c.Host, c.HostID(), tier, settings, names, p); err != nil {
		return nil, tier, err
	}
	return intent, tier, nil
}

func (r *GrantRequest) bindJoinIntent(connection *Conn, host, hostID, parent string, p *SandboxPolicy, snapshot *api.SandboxSnapshot, names []string, selected bool) (*joinGrantIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.join == nil {
		r.join = &joinGrantIntent{host: host, hostID: hostID, connection: connection, savedHost: cloneGrantHost(connection.Host), parent: parent,
			creation: cloneSandboxPolicy(p), snapshot: cloneGrantSnapshot(snapshot)}
	}
	i := r.join
	if !SameEntry(i.savedHost, connection.Host) || i.host != host || i.hostID != hostID || i.parent != parent ||
		!sameSandboxCreation(i.creation, p) || !sameGrantSnapshot(i.snapshot, snapshot) ||
		selected && i.selected && !sameGrantNames(i.names, names) {
		return nil, joinIntentError()
	}
	if selected && !i.selected {
		i.names, i.selected = slices.Clone(names), true
	}
	i.connection = connection
	return cloneJoinGrantIntent(i), nil
}

func cloneJoinGrantIntent(i *joinGrantIntent) *joinGrantIntent {
	if i == nil {
		return nil
	}
	q := *i
	q.savedHost = cloneGrantHost(i.savedHost)
	q.creation, q.snapshot, q.names = cloneSandboxPolicy(i.creation), cloneGrantSnapshot(i.snapshot), slices.Clone(i.names)
	return &q
}
