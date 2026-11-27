package client

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type rewindValueScopeKeyV1 struct{}

type rewindValueScopeV1 struct {
	plan  *RewindPreparation
	id    string
	names []string
}

// This mode describes fresh delivery for an actual retained live rewind
// origin. It never claims that the old runtime is already absent or cold.
func (p *RewindPreparation) preparationModeV1(ctx context.Context, c *Conn, id string, remote bool) (string, error) {
	if p == nil || p.conn != c || p.selection.ID != id {
		return "", rewindSourceErrorV1()
	}
	if remote {
		if err := p.checkRemote(ctx); err != nil {
			return "", err
		}
	} else if err := p.checkLocal(ctx); err != nil {
		return "", err
	}
	p.mu.Lock()
	prepared := p.prepared
	p.mu.Unlock()
	if prepared == nil || !prepared.Prepared.Valid() {
		return "", rewindSourceErrorV1()
	}
	return "rewind", nil
}

func (p *RewindPreparation) checkMemberValuesV1(ctx context.Context, h Host, id string, names []string) error {
	scope, _ := ctx.Value(rewindValueScopeKeyV1{}).(*rewindValueScopeV1)
	if scope == nil || scope.plan != p || scope.id != id {
		return rewindSourceErrorV1()
	}
	if !SameEntry(p.conn.Host, h) {
		return rewindSourceErrorV1()
	}
	if err := p.checkRemote(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	member, ok := p.group.members[id]
	p.mu.Unlock()
	canonical, err := canonicalGrantNames(names)
	if err != nil || !ok || !slices.Equal(member.names, canonical) || !slices.Equal(scope.names, canonical) {
		return rewindSourceErrorV1()
	}
	return p.checkDeliveryGrantsV1(ctx)
}

func revalidateRewindValueSourceV1(ctx context.Context) error {
	if p := originalRewindV1(ctx); p != nil {
		scope, _ := ctx.Value(rewindValueScopeKeyV1{}).(*rewindValueScopeV1)
		if scope == nil || scope.plan != p {
			return rewindSourceErrorV1()
		}
		if err := p.checkRemote(ctx); err != nil {
			return err
		}
		return p.checkDeliveryGrantsV1(ctx)
	}
	return nil
}

// The complete member, key and classifier gates precede the first value
// command. Rechecks cannot donate an earlier tier's live answer to a new one.
func (p *RewindPreparation) checkDeliveryGrantsV1(ctx context.Context) error {
	if p.named != nil {
		if err := p.named.check(ctx, p.conn, true); err != nil {
			return err
		}
	}
	_, err := checkTrustedJudgeResumeBeforeValuesV1(ctx, p.conn, p.selection.ID)
	return err
}

func (p *RewindPreparation) fetchValues(ctx context.Context) error {
	if err := p.checkRemote(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	group := cloneResumeGrantIntent(p.group)
	p.mu.Unlock()
	if group == nil {
		return rewindSourceErrorV1()
	}
	if err := p.checkDeliveryGrantsV1(ctx); err != nil {
		return err
	}
	for _, id := range append([]string{group.snapshot.Primary}, group.snapshot.Members...) {
		if err := p.checkRemote(ctx); err != nil {
			return err
		}
		p.mu.Lock()
		_, fetched := p.values[id]
		p.mu.Unlock()
		if fetched {
			continue
		}
		memberCtx := context.WithValue(ctx, rewindValueScopeKeyV1{}, &rewindValueScopeV1{plan: p, id: id, names: slices.Clone(group.members[id].names)})
		values, err := FetchSessionSecrets(memberCtx, p.conn.Host, id, group.members[id].names, p.options.Terminal)
		if err != nil {
			return err
		}
		if err := p.checkRemote(ctx); err != nil {
			clear(values)
			return err
		}
		p.mu.Lock()
		p.values[id] = values
		p.mu.Unlock()
	}
	if p.named != nil {
		if err := p.named.fetch(ctx, p.conn); err != nil {
			return err
		}
	}
	p.mu.Lock()
	selectedValues := maps.Clone(p.values[p.selection.ID])
	p.mu.Unlock()
	defer clear(selectedValues)
	if err := fetchTrustedJudgeResumeV1(ctx, p.conn, p.selection.ID, selectedValues, p.options.Terminal); err != nil {
		return err
	}
	return p.checkRemote(ctx)
}

func (p *RewindPreparation) checkStartMetadataV1(start ResumeArgs) error {
	if p == nil || start.Prepared != nil || start.ID != p.selection.ID {
		return rewindSourceErrorV1()
	}
	digest, err := api.RewindPreparationDigestV1(p.selection, start, p.floor)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prepared == nil || digest != p.digest || p.prepared.StartDigest != digest || !sameGrantSnapshot(start.Snapshot, p.group.snapshot) {
		return rewindSourceErrorV1()
	}
	return nil
}

func (p *RewindPreparation) arguments(ctx context.Context) (context.Context, api.RewindCommitArgsV1, error) {
	if err := p.checkRemote(ctx); err != nil {
		return ctx, api.RewindCommitArgsV1{}, err
	}
	p.mu.Lock()
	start := cloneResumeValues(p.start)
	group := cloneResumeGrantIntent(p.group)
	for _, id := range append([]string{group.snapshot.Primary}, group.snapshot.Members...) {
		values, fetched := p.values[id]
		if !fetched {
			p.mu.Unlock()
			return ctx, api.RewindCommitArgsV1{}, rewindSourceErrorV1()
		}
		if id == group.snapshot.Primary {
			start.Secrets = maps.Clone(values)
		} else {
			start.Members = append(start.Members, api.MemberResumeArgs{ID: id, Secrets: maps.Clone(values)})
		}
	}
	prepared := api.ClonePreparedRewindV1(p.prepared.Prepared)
	p.mu.Unlock()
	if start.Members == nil {
		start.Members = []api.MemberResumeArgs{}
	}
	if p.named != nil {
		if err := p.named.check(ctx, p.conn, true); err != nil {
			return ctx, api.RewindCommitArgsV1{}, err
		}
		p.named.mu.Lock()
		keys := &api.NamedSSHKeysV1{Version: 1, Names: slices.Clone(p.named.names), Values: maps.Clone(p.named.values)}
		fetched := p.named.fetched
		p.named.mu.Unlock()
		if len(keys.Names) != 0 {
			if !fetched || !keys.Valid() {
				return ctx, api.RewindCommitArgsV1{}, namedSSHKeySourceError()
			}
			start.NamedSSHKeys = keys
		}
		ctx = context.WithValue(ctx, resumeNamedSSHKeyPublicationKey{}, p.named)
	}
	var err error
	ctx, start, err = p.image.arguments(ctx, start)
	if err != nil {
		return ctx, api.RewindCommitArgsV1{}, err
	}
	start, err = applyTrustedJudgeResumeV1(ctx, p.conn, start)
	if err != nil {
		return ctx, api.RewindCommitArgsV1{}, err
	}
	start.Layers = api.ProjectClientImagePolicy(api.ProjectClientInputSources(start.Layers))
	args := api.RewindCommitArgsV1{V: 1, Prepared: prepared, Selection: p.selection, Start: start}
	if args.Validate() != nil {
		return ctx, api.RewindCommitArgsV1{}, rewindSourceErrorV1()
	}
	return context.WithValue(ctx, rewindPublicationKeyV1{}, p), args, nil
}

// Commit can publish only this object's actual fresh value set. No caller
// supplies a Resume DTO, token, child values or named-key carrier to it.
func (p *RewindPreparation) Commit(ctx context.Context) (api.RewindResult, error) {
	owned, finish, err := p.begin(ctx)
	if err != nil {
		return api.RewindResult{}, err
	}
	defer finish()
	base := owned // The actual pre-publication phase retains its original guards.
	p.mu.Lock()
	attempted := p.commitAttempted || p.committed
	witness := p.publication
	p.mu.Unlock()
	if attempted {
		if witness != nil {
			return witness.finishAcknowledgedV3(base)
		}
		return api.RewindResult{}, rewindSourceErrorV1()
	}
	owned, args, err := p.arguments(owned)
	if err != nil {
		return api.RewindResult{}, err
	}
	publication, release, err := p.conn.beginGrant(owned, "rewind", false)
	if err != nil {
		return api.RewindResult{}, err
	}
	defer release()
	tier, err := p.conn.checkResumeGrants(publication, args.Start)
	if err != nil {
		return api.RewindResult{}, err
	}
	if err := p.conn.CheckResumePolicyGrants(publication, tier, args.Start.Layers); err != nil {
		return api.RewindResult{}, err
	}
	if err := recordSecretNames(publication, p.conn.Host, p.conn.HostID(), tier, args.Start.Secrets); err != nil {
		return api.RewindResult{}, err
	}
	for _, member := range args.Start.Members {
		if err := recordSecretNames(publication, p.conn.Host, p.conn.HostID(), tier, member.Secrets); err != nil {
			return api.RewindResult{}, err
		}
	}
	witness, err = p.newPublicationWitnessV3(base, publication)
	if err != nil {
		return api.RewindResult{}, err
	}
	p.mu.Lock()
	p.publication = witness
	p.mu.Unlock()
	var result api.RewindResult
	if err := p.conn.callWithSent(publication, api.OpRewindCommitV1, args, &result, release); err != nil {
		return result, err
	}
	return witness.finishAcknowledgedV3(base)
}

func (n *resumeNamedSSHKeyPlan) checkRewindAcknowledgedV1(view *api.NamedSSHKeyViewV1) error {
	n.mu.Lock()
	names := slices.Clone(n.names)
	n.mu.Unlock()
	if view == nil && len(names) == 0 {
		return nil
	}
	if view == nil || view.V != 1 || !slices.Equal(view.Installed, names) {
		return namedSSHKeySourceError()
	}
	return nil
}

func (n *resumeNamedSSHKeyPlan) beforeRewindRequestV1(ctx context.Context, c *Conn, request proto.Request, arguments any) error {
	args, ok := arguments.(api.RewindCommitArgsV1)
	if !ok || n == nil || n.rewind == nil || originalRewindV1(ctx) != n.rewind || n.rewind.named != n || request.Op != api.OpRewindCommitV1 {
		return namedSSHKeySourceError()
	}
	if err := n.check(ctx, c, true); err != nil {
		return err
	}
	if err := n.rewind.checkStartMetadataV1(args.Start); err != nil {
		return err
	}
	encoded, err := json.Marshal(args)
	if err != nil || string(encoded) != string(request.Args) {
		return namedSSHKeySourceError()
	}
	n.mu.Lock()
	names, values, fetched := slices.Clone(n.names), maps.Clone(n.values), n.fetched
	ceiling := n.ceiling
	n.mu.Unlock()
	defer clear(values)
	if len(names) == 0 {
		if args.Start.NamedSSHKeys != nil {
			return namedSSHKeySourceError()
		}
	} else {
		expected := &api.NamedSSHKeysV1{Version: 1, Names: names, Values: values}
		if !fetched || !expected.Valid() || !reflect.DeepEqual(args.Start.NamedSSHKeys, expected) {
			return namedSSHKeySourceError()
		}
	}
	tier, hid, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return err
	}
	tier = min(tier, ceiling, n.creation.Ceiling, requestCeiling(ctx, tier))
	for _, name := range names {
		if err := recordRequestGrant(ctx, c.Host, hid, tier, Outbound{Kind: OutboundSSHKey, Name: name}); err != nil {
			return err
		}
	}
	return n.check(ctx, c, true)
}
