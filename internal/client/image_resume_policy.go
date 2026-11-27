package client

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// This original operation is published only after the actual policy verifier
// accepted the locally retained creation image. Public ExpectedImageV1 bytes
// or host session rows cannot construct it.
type resumeImagePolicyV1 struct {
	request   *GrantRequest
	conn      *Conn
	id        string
	creation  *SandboxPolicy
	use       *imagePolicyUseV1
	selection *sandboxImageSelectionV1
	receipt   imagePolicyReceipt
	expected  json.RawMessage
}
type resumeImagePublicationKeyV1 struct{}
type resumeImageFetchKeyV1 struct{}
type resumeImageSettingsKeyV1 struct{}

func (p *resumeImagePolicyV1) check(ctx context.Context) error {
	if p == nil || p.request != grantRequestOf(ctx) {
		return imageSelectionHistoryError()
	}
	if err := p.use.check(ctx, p.conn); err != nil {
		return err
	}
	current, err := LoadSandboxPolicy(ctx, p.conn.Host, p.id)
	if err != nil {
		return err
	}
	if !sameSandboxCreation(current, p.creation) {
		return imageSelectionHistoryError()
	}
	return nil
}

func resumeImageLayersV1(ctx context.Context, operation *settingsOperation, h Host, creation *SandboxPolicy) ([]api.Layer, error) {
	if creation == nil {
		settings, err := operation.Settings(ctx)
		if err != nil {
			return nil, err
		}
		return []api.Layer{{Kind: api.LayerAll, LayerValues: settings.LayerValues, Lock: policyLock(settings.Lock)}, {Kind: api.LayerHost, Name: h.Name, LayerValues: h.LayerValues, Lock: policyLock(h.Lock)}}, nil
	}
	if len(creation.PolicySources) == 0 {
		return retainedNewImagePolicyLayersV1(ctx, operation, h, NewArgs{})
	}
	pin, err := api.DecodePolicySourcePin(creation.PolicySources)
	if err != nil || pin == nil {
		return nil, policySourceStateError()
	}
	layers := make([]api.Layer, 0, len(pin.Layers))
	for _, ref := range pin.Layers {
		layer := api.Layer{Kind: ref.Kind, Name: ref.Name, Source: ref.Source}
		if ref.Kind == api.LayerSession {
			layer.LayerValues = api.CloneLayerValues(pin.Session)
		}
		layers = append(layers, layer)
	}
	return retainedNewImagePolicyLayersV1(ctx, operation, h, NewArgs{Layers: layers})
}

func captureResumeImagePolicy(ctx context.Context, c *Conn, id string) (context.Context, *resumeImagePolicyV1, error) {
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return ctx, nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return ctx, nil, err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return ctx, nil, grantOperationError()
	}
	r.mu.Lock()
	before := r.resumeImage
	r.mu.Unlock()
	if before != nil {
		if before.conn != c || before.id != id {
			return ctx, nil, imageSelectionHistoryError()
		}
		owned := context.WithValue(ctx, imagePolicyUseKeyV1{}, before.use)
		return owned, before, before.check(owned)
	}
	creation, err := LoadSandboxPolicy(ctx, c.Host, id)
	if err != nil {
		return ctx, nil, err
	}
	if creation != nil && creation.CreationWall != "microvm" {
		return ctx, nil, nil
	}
	if !ValidHostID(c.HostID()) || creation != nil && (creation.HostID != c.HostID() || creation.FutureLineage) {
		return ctx, nil, imageSelectionHistoryError()
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return ctx, nil, err
	}
	layers, err := resumeImageLayersV1(ctx, operation, c.Host, creation)
	if err != nil {
		return ctx, nil, err
	}
	policy, err := imagePolicyValuesForLayersV1(ctx, operation, layers)
	if err != nil {
		return ctx, nil, err
	}
	use := &imagePolicyUseV1{request: r, family: grantOpResume, layers: api.CloneLayers(layers), conn: c, stamp: c.hostTransport, source: c.hostTransport.source, hostID: c.HostID(), operation: operation, policy: policy}
	owned := context.WithValue(ctx, imagePolicyUseKeyV1{}, use)
	if err := use.check(owned, c); err != nil {
		return ctx, nil, err
	}
	var history json.RawMessage
	if creation != nil {
		history = creation.imageSelectionRaw
	}
	selection, err := decodeSandboxImageSelection(history)
	if err != nil {
		return ctx, nil, err
	}
	if selection != nil && !c.HasFeature(api.FeatureResumeImageV1) {
		return ctx, nil, c.grantHelperOld("compare the original image before resume")
	}
	receipt, err := verifyHistoricalImagePolicy(owned, c, selection, policy)
	if err != nil {
		return ctx, nil, err
	}
	p := &resumeImagePolicyV1{request: r, conn: c, id: id, creation: cloneSandboxPolicy(creation), use: use, selection: cloneSandboxImageSelection(selection), receipt: receipt}
	if selection != nil {
		encoded, err := vmneed.EncodeImageV1(selection.Image)
		if err != nil {
			return ctx, nil, err
		}
		p.expected = bytes.Clone(encoded)
	}
	if err := p.check(owned); err != nil {
		return ctx, nil, err
	}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return ctx, nil, err
	}
	if r.resumeImage != nil {
		r.mu.Unlock()
		return ctx, nil, grantOperationError()
	}
	r.resumeImage, r.imagePolicy = p, use
	r.mu.Unlock()
	return owned, p, nil
}

func (p *resumeImagePolicyV1) arguments(ctx context.Context, a ResumeArgs) (context.Context, ResumeArgs, error) {
	owned, a, err := p.preparationArgumentsV1(ctx, a)
	if err != nil || p == nil {
		return owned, a, err
	}
	return context.WithValue(owned, resumeImagePublicationKeyV1{}, p), a, nil
}

// Preparation keeps the original image use without installing a final-frame
// publication guard. No caller clears an existing guard to send another op.
func (p *resumeImagePolicyV1) preparationArgumentsV1(ctx context.Context, a ResumeArgs) (context.Context, ResumeArgs, error) {
	if p == nil {
		if len(a.ExpectedImageV1) != 0 {
			return ctx, a, imageSelectionHistoryError()
		}
		return ctx, a, nil
	}
	if a.ID != p.id || len(a.ExpectedImageV1) != 0 && !bytes.Equal(a.ExpectedImageV1, p.expected) {
		return ctx, a, imageSelectionHistoryError()
	}
	owned := context.WithValue(ctx, imagePolicyUseKeyV1{}, p.use)
	if err := p.check(owned); err != nil {
		return ctx, a, err
	}
	a.ExpectedImageV1 = bytes.Clone(p.expected)
	return owned, a, nil
}

func (p *resumeImagePolicyV1) beforeRequest(ctx context.Context, c *Conn, request proto.Request, args any) error {
	held, owns := ctx.Value(heldKey{c.Host.Name}).(*heldLock)
	if !owns || held.gone.Load() {
		return imageSelectionHistoryError()
	}
	a, ok := args.(ResumeArgs)
	if request.Op == api.OpRewindCommitV1 {
		commit, selected := args.(api.RewindCommitArgsV1)
		if !selected || originalRewindV1(ctx) == nil {
			return imageSelectionHistoryError()
		}
		a, ok = commit.Start, true
	}
	if !ok || p.conn != c || request.Op != "resume" && request.Op != api.OpRewindCommitV1 || a.ID != p.id || !bytes.Equal(a.ExpectedImageV1, p.expected) {
		return imageSelectionHistoryError()
	}
	encoded, err := json.Marshal(args)
	if err != nil || !bytes.Equal(encoded, request.Args) {
		return imageSelectionHistoryError()
	}
	return p.check(ctx)
}

// The global secret helper can borrow only this actual earlier constructor.
// It cannot open another settings source or verify a caller-selected digest.
func resumeImageBeforeValues(ctx context.Context, h Host, id string, creation *SandboxPolicy) (context.Context, *settingsOperation, error) {
	if creation != nil && creation.CreationWall != "microvm" {
		return ctx, nil, nil
	}
	r := grantRequestOf(ctx)
	if r == nil || requestGrantOperation(r) != grantOpResume {
		return ctx, nil, nil
	}
	r.mu.Lock()
	p := r.resumeImage
	r.mu.Unlock()
	if p == nil && creation == nil {
		operation, err := r.borrowNewSettings(ctx)
		if err != nil {
			return ctx, nil, err
		}
		layers, err := resumeImageLayersV1(ctx, operation, h, nil)
		if err != nil {
			return ctx, nil, err
		}
		policy, err := imagePolicyValuesForLayersV1(ctx, operation, layers)
		if err != nil {
			return ctx, nil, err
		}
		if err := validateImagePolicy(policy); err != nil {
			return ctx, nil, err
		}
		if policy != nil && imagePolicyString(policy.Identity) != "" {
			return ctx, nil, imageSelectionHistoryError()
		}
		return context.WithValue(ctx, resumeImageSettingsKeyV1{}, operation), operation, nil
	}
	if p == nil || !SameEntry(h, p.conn.Host) || (creation == nil) != (p.creation == nil) {
		return ctx, nil, imageSelectionHistoryError()
	}
	if creation != nil && (creation.HostID != p.creation.HostID || !bytes.Equal(creation.imageSelectionRaw, p.creation.imageSelectionRaw)) {
		return ctx, nil, imageSelectionHistoryError()
	}
	if id != p.id {
		r.mu.Lock()
		group := r.resume
		r.mu.Unlock()
		if group == nil || group.snapshot == nil || id != group.snapshot.Primary && group.members[id].creation == nil {
			return ctx, nil, imageSelectionHistoryError()
		}
	}
	owned := context.WithValue(ctx, imagePolicyUseKeyV1{}, p.use)
	if err := p.check(owned); err != nil {
		return ctx, nil, err
	}
	return context.WithValue(owned, resumeImageFetchKeyV1{}, p), p.use.operation, nil
}

func revalidateResumeImageValueSourceV1(ctx context.Context) error {
	if p, _ := ctx.Value(resumeImageFetchKeyV1{}).(*resumeImagePolicyV1); p != nil {
		return p.check(ctx)
	}
	if operation, _ := ctx.Value(resumeImageSettingsKeyV1{}).(*settingsOperation); operation != nil {
		return operation.Revalidate(ctx)
	}
	return nil
}
