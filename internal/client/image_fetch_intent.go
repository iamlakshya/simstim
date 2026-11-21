package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
)

// imageResolutionV1 is issued only by the actual private resolver. A copied
// Request or release label cannot create this original source relationship.
type imageResolutionV1 struct {
	use            *imagePolicyUseV1
	input          string
	request        image.Request
	release        bool
	releaseVersion string
}

type imageFetchPublicationKeyV1 struct{}
type imageFetchCleanupKeyV1 struct{}

// imageFetchIntentV1 owns one approved missing-image acquisition in the same
// original image operation. The normal grant question map holds its one answer.
type imageFetchIntentV1 struct {
	mu                 sync.Mutex
	turnMu             sync.Mutex
	ctx                context.Context
	request            *GrantRequest
	conn               *Conn
	use                *imagePolicyUseV1
	resolved           resolvedImage
	mode               string
	scope              grantScope
	ceiling            Tier
	policyHash         string
	closed             bool
	closing            bool
	usedHost           bool
	usedLaptop         bool
	complete           bool
	hostToken          string
	installToken       string
	installCorrelation string
	ops                []*imageFetchCallV1
	turns              map[uint64]*imageFetchCallV1
	planned            *imageFetchCallV1
	blobs              map[string]int64
	offsets            map[string]int64
	uncertain          bool
	work               context.Context
	cancel             context.CancelFunc
	busy               bool
	finished           chan struct{}
	closedTokens       map[string]bool
	aborts             map[string]*imageFetchAbortV1
}

func cloneResolvedImageV1(value resolvedImage) resolvedImage {
	value.request = value.request.Clone()
	if value.layers != nil {
		value.layers = append([]image.Layer{}, value.layers...)
	}
	if value.documents != nil {
		documents := make([]image.Document, len(value.documents))
		for i, doc := range value.documents {
			documents[i] = image.Document{Digest: doc.Digest, Data: append([]byte(nil), doc.Data...)}
		}
		value.documents = documents
	}
	return value
}

func (p *imageFetchIntentV1) check(ctx context.Context, c *Conn) error {
	if p == nil || c == nil || p.conn != c || p.request != grantRequestOf(ctx) || p.use == nil || p.use.request != p.request || p.use.conn != c {
		return imageFetchIntentError()
	}
	if err := requireImageFetchFamilyV1(ctx, c, p.use.family); err != nil {
		return err
	}
	if err := p.use.check(ctx, c); err != nil {
		return err
	}
	tier, hid, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return err
	}
	if hid != p.scope.hostID || tier != p.scope.tier || requestCeiling(ctx, tier) != p.ceiling {
		return imageFetchIntentError()
	}
	if !p.resolved.release {
		policy, err := readImageCertificatePolicy(ctx, p.use.policy)
		if err != nil {
			return err
		}
		if policy.hash != p.policyHash {
			return imageFetchIntentError()
		}
	}
	p.mu.Lock()
	closed := p.closed || p.closing
	uncertain := p.uncertain
	p.mu.Unlock()
	if closed {
		return imageFetchIntentError()
	}
	if uncertain {
		return imageFetchIncompleteError()
	}
	return ctx.Err()
}

func (p *imageFetchIntentV1) confirmed(ctx context.Context, c *Conn) error {
	if err := p.check(ctx, c); err != nil {
		return err
	}
	p.request.mu.Lock()
	yes := p.request.confirmed[p.scope]
	err := p.request.lifetimeErrorLocked()
	p.request.mu.Unlock()
	if err != nil {
		return err
	}
	if !yes {
		return p.question()
	}
	return nil
}

func (p *imageFetchIntentV1) question() error {
	ref := p.resolved.issued.input
	label := CleanLine(ref) + "@" + p.resolved.request.Digest[7:19]
	if p.resolved.release {
		label = "agent-base@" + p.resolved.request.Digest[7:19]
	}
	size := ""
	if p.resolved.request.DataBlocks > 0 {
		size = fmt.Sprintf(" (%d bytes)", p.resolved.request.DataBlocks*4096)
	}
	return &scopedGrantConfirmation{request: p.request, scope: p.scope, ConfirmError: &ConfirmError{
		Prompt: fmt.Sprintf("%s lacks %s%s. Get it now?", CleanLine(p.conn.Host.Name), label, size), Kind: ConfirmYesNo, Host: p.conn.Host.Name,
		Grant: Outbound{Kind: OutboundImageFetchV1, Name: label}, Err: &UserError{Msg: fmt.Sprintf("%s lacks the pinned image %s", CleanLine(p.conn.Host.Name), label), Hint: brand.Name + " image pull " + ShellWord(ref) + " " + ShellWord(p.conn.Host.Name), Code: "image_fetch_required", Kind: KindRefused},
	}}
}

// prepareNewImageFetch runs only after this original request observed a cache
// miss and before layers are fetched or a host conversion is requested.
func prepareNewImageFetch(ctx context.Context, c *Conn, resolved *resolvedImage, mode string) (context.Context, error) {
	r := grantRequestOf(ctx)
	if r == nil {
		return ctx, nil
	}
	family := requestGrantOperation(r)
	if family != grantOpNew && family != grantOpSandboxPrepare {
		return ctx, nil
	}
	if err := requireImageFetchFamilyV1(ctx, c, family); err != nil {
		return ctx, err
	}
	use, _ := ctx.Value(imagePolicyUseKeyV1{}).(*imagePolicyUseV1)
	if resolved == nil || resolved.issued == nil || resolved.issued.use != use || use == nil || use.request != r || use.conn != c || !reflect.DeepEqual(resolved.request, resolved.issued.request) || resolved.release != resolved.issued.release || resolved.releaseVersion != resolved.issued.releaseVersion || resolved.request.SavedHost != nil || resolved.request.Transfer != "" || resolved.request.Validate() != nil {
		return ctx, imageFetchIntentError()
	}
	if err := use.check(ctx, c); err != nil {
		return ctx, err
	}
	if use.family != family {
		return ctx, imageFetchIntentError()
	}
	if family == grantOpSandboxPrepare && (!resolved.release || resolved.issued.input != "default" || resolved.releaseVersion == "") {
		return ctx, imageFetchIntentError()
	}

	if mode != "auto" && mode != "host" && mode != "laptop" {
		return ctx, imageFetchIntentError()
	}
	tier, hid, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return ctx, err
	}
	if hid != c.HostID() {
		return ctx, imageFetchIntentError()
	}
	policyHash := ""
	if !resolved.release {
		receipt, err := verifyImagePolicy(ctx, resolved.request, resolved.registry, use.policy)
		if err != nil {
			return ctx, err
		}
		policyHash = receipt.policy
		if err := recordImagePolicyReceipt(ctx, resolved.request, receipt); err != nil {
			return ctx, err
		}
	}
	encoded, err := json.Marshal(struct {
		Request                      image.Request
		Mode, Input, Release, Policy string
	}{resolved.request, mode, resolved.issued.input, resolved.releaseVersion, policyHash})
	if err != nil {
		return ctx, fmt.Errorf("encode the original image fetch selection: %w", err)
	}
	sum := sha256.Sum256(encoded)
	work, cancel := context.WithCancel(ctx)
	candidate := &imageFetchIntentV1{ctx: ctx, work: work, cancel: cancel, request: r, conn: c, use: use, resolved: cloneResolvedImageV1(*resolved), mode: mode, policyHash: policyHash, turns: map[uint64]*imageFetchCallV1{}, ceiling: requestCeiling(ctx, tier), closedTokens: map[string]bool{}, aborts: map[string]*imageFetchAbortV1{}, scope: grantScope{host: c.Host.Name, hostID: hid, tier: tier, kind: OutboundImageFetchV1, name: hex.EncodeToString(sum[:]), purpose: requestGrantPurpose(r), operation: family}}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		cancel()
		return ctx, err
	}
	before := r.imageFetch
	if before == nil {
		r.imageFetch = candidate
		before = candidate
	}
	r.mu.Unlock()
	if before != candidate {
		cancel()
	}
	if before.conn != c || before.use != use || before.mode != mode || before.scope != candidate.scope {
		return ctx, imageFetchIntentError()
	}
	if err := before.confirmed(ctx, c); err != nil {
		return ctx, err
	}
	before.mu.Lock()
	if err := ctx.Err(); err != nil {
		before.mu.Unlock()
		return ctx, err
	}
	if before.complete || before.busy || before.closed || before.closing || before.uncertain {
		before.mu.Unlock()
		return ctx, imageFetchIntentError()
	}
	before.busy, before.finished = true, make(chan struct{})
	*resolved = cloneResolvedImageV1(before.resolved)
	before.mu.Unlock()
	return context.WithValue(before.work, imageFetchPublicationKeyV1{}, before), nil
}

func (p *imageFetchIntentV1) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		p.busy = false
		close(p.finished)
	}
}

func imageFetchIntentError() *UserError {
	return &UserError{Msg: "the missing image fetch does not match this original approved image operation", Hint: "Start a new request and approve its exact missing image in a terminal.", Code: "image_fetch_scope", Kind: KindRefused}
}

func checkImageFetchBoundaryV1(ctx context.Context, c *Conn) error {
	if intent, _ := ctx.Value(imageFetchPublicationKeyV1{}).(*imageFetchIntentV1); intent != nil {
		return intent.confirmed(ctx, c)
	}
	return checkRetainedImagePolicy(ctx, c)
}
