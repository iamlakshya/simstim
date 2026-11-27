package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// RewindOptions contains original local choices, never grant authority.
type RewindOptions struct {
	PassedSecrets []string
	Terminal      bool
}

// RewindPreparation owns one original accepted Source, H5 request, complete
// member selection and distinct live rewind source. A token cannot create it.
type RewindPreparation struct {
	mu              sync.Mutex
	work            sync.Mutex
	request         *GrantRequest
	conn            *Conn
	stamp           *hostTransportStamp
	lifetime        context.Context
	cancel          context.CancelFunc
	selection       api.RewindArgs
	options         RewindOptions
	floor           string
	start           ResumeArgs
	metadata        []byte
	digest          string
	group           *resumeGrantIntent
	image           *resumeImagePolicyV1
	named           *resumeNamedSSHKeyPlan
	prepared        *api.RewindPrepareResultV1
	beforeCohort    *api.SandboxCohortView
	publication     *rewindPublicationWitnessV3
	attempted       bool
	commitAttempted bool
	committed       bool
	closing         bool
	closed          bool
	values          map[string]map[string]api.Secret
}

func (*RewindPreparation) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func originalRewindV1(ctx context.Context) *RewindPreparation {
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rewind
}

func rewindSourceErrorV1() error {
	return &UserError{Code: "rewind_source", Msg: "the original rewind source, grants or selection changed", Hint: "Start a new rewind request for the exact session and checkpoint.", Kind: KindRefused}
}

// PrepareRewind selects and fetches through the existing real resume grant
// producers after the host retains its original live rewind source. A retry
// retains the same request, keys, metadata and checkpoint; Close is required.
func (c *Conn) PrepareRewind(ctx context.Context, selection api.RewindArgs, options RewindOptions) (*RewindPreparation, error) {
	if selection.Validate() != nil {
		return nil, rewindSourceErrorV1()
	}
	if c == nil || !c.HasFeature(api.FeatureRewind) || !c.HasFeature(api.FeatureRewindPreparationV1) {
		return nil, forkCheckpointUpgrade("prepare an original rewind")
	}
	ctx, r := ensureGrantRequest(ctx)
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	passed, err := canonicalGrantNames(options.PassedSecrets)
	if err != nil {
		return nil, err
	}
	options.PassedSecrets = passed
	r.mu.Lock()
	p := r.rewind
	if p == nil {
		if r.namedResume != nil || r.resumeImage != nil || r.resume != nil || r.legacyResume != nil || r.join != nil || r.freshChild != nil || r.requestContext == nil {
			r.mu.Unlock()
			return nil, rewindSourceErrorV1()
		}
		lifetime, cancel := context.WithCancel(r.requestContext)
		p = &RewindPreparation{request: r, conn: c, stamp: c.hostTransport, lifetime: lifetime, cancel: cancel, selection: selection, options: options, floor: c.Host.WallFloor, values: map[string]map[string]api.Secret{}}
		r.rewind = p
	}
	r.mu.Unlock()
	if p.conn != c || p.stamp != c.hostTransport || p.selection != selection || p.options.Terminal != options.Terminal || !slices.Equal(p.options.PassedSecrets, options.PassedSecrets) {
		return nil, rewindSourceErrorV1()
	}
	owned, finish, err := p.begin(ctx)
	if err != nil {
		return p, err
	}
	defer finish()
	if !options.Terminal {
		owned = WithoutInput(owned)
	}
	p.mu.Lock()
	witness := p.publication
	p.mu.Unlock()
	if witness != nil && witness.hasAcknowledgementV3() {
		return p, p.checkIdentity(owned) // Retry only the actual acknowledged local publication.
	}
	if err := p.prepare(owned); err != nil {
		return p, err
	}
	if err := p.fetchValues(owned); err != nil {
		return p, err
	}
	return p, nil
}

func (p *RewindPreparation) prepare(ctx context.Context) error {
	if err := p.checkIdentity(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	prepared, attempted := p.prepared, p.attempted
	p.mu.Unlock()
	if prepared != nil {
		return p.checkRemote(ctx)
	}
	if attempted {
		return rewindSourceErrorV1()
	}
	policy, err := p.conn.PlanResumePolicy(ctx, p.selection.ID)
	if err != nil {
		return err
	}
	rows, err := p.conn.Sessions(ctx)
	if err != nil {
		return err
	}
	var row *Session
	for index := range rows {
		if rows[index].ID == p.selection.ID {
			row = &rows[index]
			break
		}
	}
	if row == nil || row.Wall == nil || row.Wall.Level != "microvm" {
		return rewindSourceErrorV1()
	}
	names, err := ResumePlanSecretNames(p.conn.Host, *row, p.options.PassedSecrets, policy)
	if err != nil {
		return err
	}
	if err := p.conn.CheckResumeGrants(ctx, p.selection.ID, names); err != nil {
		return err
	}
	if err := p.conn.CheckResumePolicyBeforeFetch(ctx, p.selection.ID, policy); err != nil {
		return err
	}
	group, tier, err := p.conn.prepareResumeMembers(ctx, p.selection.ID, names)
	if err != nil {
		return err
	}
	for _, member := range group.members {
		if member.creation != nil && (member.creation.futureDeliveryReceipt || member.creation.futureInputOperation) {
			return futureSandboxReceiptError()
		}
	}
	before, err := p.conn.readFreshChildCohort(ctx, p.selection.ID)
	if err != nil {
		return err
	}
	if !sameGrantSnapshot(before.Snapshot, group.snapshot) || !ValidHostID(before.RunGeneration) || before.SecretState == nil || !ValidHostID(before.SecretState.Generation) {
		return rewindSourceErrorV1()
	}
	if _, err := ChooseWall(p.conn.Host, tier, "microvm", false); err != nil {
		return err
	}
	owned, image, err := captureResumeImagePolicy(ctx, p.conn, p.selection.ID)
	if err != nil || image == nil {
		if err != nil {
			return err
		}
		return rewindSourceErrorV1()
	}
	start := ResumeArgs{ID: p.selection.ID, Layers: api.CloneLayers(policy.Layers), Snapshot: cloneGrantSnapshot(group.snapshot)}
	owned, start, err = image.preparationArgumentsV1(owned, start)
	if err != nil {
		return err
	}
	metadata, err := api.RewindPreparationMetadataV1(p.selection, start, p.floor)
	if err != nil || metadata.Validate() != nil {
		return rewindSourceErrorV1()
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	digest, err := api.RewindPreparationDigestV1(p.selection, metadata.Start, p.floor)
	if err != nil {
		return err
	}
	p.request.mu.Lock()
	named := p.request.namedResume
	p.request.mu.Unlock()
	if named != nil {
		named.mu.Lock()
		if named.prepared != nil || named.nativeAttempted || named.rewind != nil && named.rewind != p {
			named.mu.Unlock()
			return rewindSourceErrorV1()
		}
		named.rewind = p
		named.mu.Unlock()
	}
	p.mu.Lock()
	p.group, p.image, p.named, p.start, p.metadata, p.digest = cloneResumeGrantIntent(group), image, named, cloneResumeValues(metadata.Start), bytes.Clone(encoded), digest
	p.beforeCohort = before.Clone()
	p.mu.Unlock()
	publication, release, err := p.conn.beginGrant(owned, "prepare rewind", false)
	if err != nil {
		return err
	}
	defer release()
	publication = context.WithValue(publication, rewindPublicationKeyV1{}, p)
	var result api.RewindPrepareResultV1
	if err := p.conn.callWithSent(publication, api.OpRewindPrepareV1, metadata, &result, release); err != nil {
		return err
	}
	if err := result.Validate(p.selection, digest, group.snapshot); err != nil {
		return rewindSourceErrorV1()
	}
	result.Prepared = api.ClonePreparedRewindV1(result.Prepared)
	result.Source = cloneGrantSnapshot(result.Source)
	p.mu.Lock()
	p.prepared = &result
	p.mu.Unlock()
	return p.checkRemote(owned)
}

func (p *RewindPreparation) checkIdentity(ctx context.Context) error {
	if p == nil || ctx == nil || p.request != grantRequestOf(ctx) || p.conn == nil || p.stamp != p.conn.hostTransport || originalRewindV1(ctx) != p || p.floor != p.conn.Host.WallFloor {
		return rewindSourceErrorV1()
	}
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, p.conn); err != nil {
		return err
	}
	p.mu.Lock()
	closed := p.closed || p.closing
	p.mu.Unlock()
	if closed {
		return rewindSourceErrorV1()
	}
	return nil
}

func (p *RewindPreparation) checkLocal(ctx context.Context) error {
	if err := p.checkIdentity(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	group, image, named := cloneResumeGrantIntent(p.group), p.image, p.named
	p.mu.Unlock()
	if group == nil || image == nil {
		return rewindSourceErrorV1()
	}
	if err := image.check(ctx); err != nil {
		return err
	}
	current, _, err := p.conn.prepareResumeMembers(ctx, p.selection.ID, group.originalNames)
	if err != nil {
		return err
	}
	if !sameRewindGrantGroupV1(current, group) {
		return rewindSourceErrorV1()
	}
	if named != nil {
		named.mu.Lock()
		valid := named.rewind == p && named.prepared == nil && !named.nativeAttempted
		named.mu.Unlock()
		if !valid {
			return rewindSourceErrorV1()
		}
		return named.check(ctx, p.conn, false)
	}
	return nil
}

func sameRewindGrantGroupV1(a, b *resumeGrantIntent) bool {
	if a == nil || b == nil || a.connection != b.connection || a.hostID != b.hostID || a.host != b.host || !SameEntry(a.savedHost, b.savedHost) || a.original != b.original || !slices.Equal(a.originalNames, b.originalNames) || !slices.Equal(a.primaryNames, b.primaryNames) || !sameGrantSnapshot(a.snapshot, b.snapshot) || len(a.members) != len(b.members) {
		return false
	}
	for id, member := range a.members {
		before, present := b.members[id]
		if !present || !sameSandboxCreation(member.creation, before.creation) || !slices.Equal(member.names, before.names) {
			return false
		}
	}
	return true
}

func (p *RewindPreparation) checkRemote(ctx context.Context) error {
	if err := p.checkLocal(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	prepared := p.prepared
	p.mu.Unlock()
	if prepared == nil {
		return rewindSourceErrorV1()
	}
	var result api.RewindPrepareResultV1
	ctx = context.WithValue(ctx, rewindPublicationKeyV1{}, p)
	args := api.RewindPreparedArgsV1{V: 1, Prepared: api.ClonePreparedRewindV1(prepared.Prepared), Selection: p.selection}
	if err := p.conn.Call(ctx, api.OpRewindPreparedCheckV1, args, &result); err != nil {
		return err
	}
	if result.Validate(p.selection, p.digest, p.group.snapshot) != nil || !reflect.DeepEqual(result, *prepared) {
		return rewindSourceErrorV1()
	}
	return p.checkLocal(ctx)
}

func (p *RewindPreparation) begin(ctx context.Context) (context.Context, func(), error) {
	if p == nil || ctx == nil || p.request == nil || p.lifetime == nil || p.cancel == nil {
		return ctx, nil, rewindSourceErrorV1()
	}
	p.work.Lock()
	p.mu.Lock()
	closed := p.closed || p.closing
	p.mu.Unlock()
	if closed {
		p.work.Unlock()
		return ctx, nil, rewindSourceErrorV1()
	}
	owned, cancel := context.WithCancel(ctx)
	joined := make(chan struct{})
	stop := context.AfterFunc(p.lifetime, func() { defer close(joined); cancel() })
	finish := func() {
		if !stop() {
			<-joined
		}
		cancel()
		p.work.Unlock()
	}
	if err := owned.Err(); err != nil {
		finish()
		return ctx, nil, err
	}
	return owned, finish, nil
}

// Close cancels and joins local producers, then cancels this same server
// preparation. A missing cleanup acknowledgement retains the original source.
func (p *RewindPreparation) Close() error {
	if p == nil {
		return nil
	}
	if p.request == nil || p.conn == nil || p.cancel == nil || p.lifetime == nil {
		return rewindSourceErrorV1()
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closing = true
	p.cancel()
	p.mu.Unlock()
	p.work.Lock()
	defer p.work.Unlock()
	p.mu.Lock()
	prepared, attempted, committed := p.prepared, p.attempted, p.committed
	p.mu.Unlock()
	if prepared == nil && attempted {
		p.conn.Close()
		return rewindSourceErrorV1()
	}
	if prepared != nil && !committed {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, grantRequestKey{}, p.request)
		ctx = context.WithValue(ctx, rewindPublicationKeyV1{}, p)
		args := api.RewindPreparedArgsV1{V: 1, Prepared: api.ClonePreparedRewindV1(prepared.Prepared), Selection: p.selection}
		if err := p.conn.Call(ctx, api.OpRewindPreparedCancelV1, args, nil); err != nil {
			p.conn.Close()
			return err
		}
	}
	p.mu.Lock()
	for _, values := range p.values {
		clear(values)
	}
	clear(p.values)
	p.closed = true
	p.mu.Unlock()
	return nil
}
