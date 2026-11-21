package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// These closed kinds distinguish request-local questions only. They cannot
// enter the grant ledger or become a guest exposure permission.
const (
	outboundTrustedJudgeV1 OutboundKind = "trusted-judge-v1"
	// OutboundTrustedJudgeOwnV1 names a request-local question, never a ledger grant.
	OutboundTrustedJudgeOwnV1 OutboundKind = "trusted-judge-own-v1"
)

// trustedJudgePlanV1 is issued only by the original local start or explicit
// cold operation. A policy DTO, secret name or matching endpoint cannot issue
// its retained settings descriptor, accepted connection or request answers.
type trustedJudgePlanV1 struct {
	armNeeded                      bool
	mu                             sync.Mutex
	request                        *GrantRequest
	conn                           *Conn
	stamp                          *hostTransportStamp
	operation                      *settingsOperation
	newPlan                        *newSecretPlan
	family                         grantOperation
	id, wall, name, command, scope string
	ceiling                        Tier
	creation                       *SandboxPolicy
	cold                           bool
	resume                         *resumeNamedSSHKeyPlan
	generation                     string
	classifier                     api.ClassifierValues
	use                            api.TrustedSecretUseV1
	definition                     SecretDefinition
	values                         map[string]api.Secret
	busy, fetched, closed          bool
}

func (*trustedJudgePlanV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func trustedJudgeSourceErrorV1() error {
	return &UserError{Msg: "the classifier credential is unavailable for this original request", Hint: "Use the original trusted classifier settings and arm its exact bound key, or configure an anonymous local model.", Code: "judge_key_source", Kind: KindRefused}
}

func trustedJudgePolicyV1(layers []api.Layer) (api.Policy, error) {
	policy, _, err := egress.MergeLayers(layers)
	if err != nil {
		return api.Policy{}, policyError(err)
	}
	if policy.Classifier.Judge == nil || *policy.Classifier.Judge == "none" {
		return policy, nil
	}
	for _, origin := range policy.Origins {
		if origin.Source == api.SourceRepositoryFile {
			switch origin.Key {
			case "classifier.judge", "classifier.model", "classifier.endpoint", "classifier.key", "classifier.command":
				return api.Policy{}, trustedJudgeSourceErrorV1()
			}
		}
	}
	return policy, nil
}

func trustedJudgeTextV1(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

// The raw endpoint remains exact metadata; authority normalisation is used
// only to check its existing HTTPS binding. No URL or key lookup is returned.
func trustedJudgeEndpointV1(raw, key string) (string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" || endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return "", trustedJudgeSourceErrorV1()
	}
	if !strings.HasSuffix(endpoint.EscapedPath(), "/chat/completions") && !strings.HasSuffix(endpoint.EscapedPath(), "/messages") {
		return "", trustedJudgeSourceErrorV1()
	}
	if key == "" {
		if endpoint.Scheme == "http" {
			ip := net.ParseIP(endpoint.Hostname())
			if endpoint.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return "", trustedJudgeSourceErrorV1()
			}
		}
		return "", nil
	}
	if endpoint.Scheme != "https" || endpoint.Port() != "" && endpoint.Port() != "443" {
		return "", trustedJudgeSourceErrorV1()
	}
	host, err := egress.NormaliseHost(endpoint.Hostname())
	if err != nil {
		return "", trustedJudgeSourceErrorV1()
	}
	return net.JoinHostPort(host, "443"), nil
}

func captureTrustedJudgeV1(ctx context.Context, c *Conn, operation *settingsOperation, layers []api.Layer, wall string, family grantOperation, id string, creation *SandboxPolicy, p *newSecretPlan) (*trustedJudgePlanV1, error) {
	if c == nil || operation == nil || c.hostTransport == nil || !ValidHostID(c.HostID()) {
		return nil, trustedJudgeSourceErrorV1()
	}
	if err := requireGrantOperation(ctx, family); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return nil, err
	}
	policy, err := trustedJudgePolicyV1(layers)
	if err != nil {
		return nil, err
	}
	classifier := policy.Classifier
	if classifier.Judge == nil || *classifier.Judge != "model" {
		return nil, nil
	}
	if classifier.Model == nil || !trustedJudgeTextV1(*classifier.Model, 256) || classifier.Endpoint == nil || !trustedJudgeTextV1(*classifier.Endpoint, 4096) {
		return nil, trustedJudgeSourceErrorV1()
	}
	name := ""
	if classifier.Key != nil {
		name = *classifier.Key
	}
	authority, err := trustedJudgeEndpointV1(*classifier.Endpoint, name)
	if err != nil {
		return nil, err
	}
	tier, hid, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return nil, err
	}
	if hid != c.HostID() {
		return nil, grantIdentityError(c.Host)
	}
	ceiling := min(tier, requestCeiling(ctx, tier))
	if creation != nil {
		ceiling = min(ceiling, creation.Ceiling)
	}
	plan := &trustedJudgePlanV1{request: grantRequestOf(ctx), conn: c, stamp: c.hostTransport, operation: operation, newPlan: p, family: family, id: id, wall: wall, name: name, ceiling: ceiling, creation: cloneSandboxPolicy(creation), classifier: api.ClonePolicy(policy).Classifier, values: map[string]api.Secret{}}
	if name != "" {
		if wall == "" || wall == "none" || !secretNameRE.MatchString(name) || name == "SSH_AUTH_SOCK" || view.Kinds[name] != "" || view.Commands[name] == "" {
			return nil, trustedJudgeSourceErrorV1()
		}
		if !c.HasFeature(api.FeatureJudgeKeyV1) || !c.HasFeature(api.FeatureBoundSecrets) || !c.HasFeature("session.id") {
			return nil, c.grantHelperOld("receive an original trusted classifier credential")
		}
		binding, ok := view.Binds[name]
		if !ok || !slices.Contains(binding.Hosts, authority) {
			return nil, trustedJudgeSourceErrorV1()
		}
		plan.command = view.Commands[name]
		plan.definition = settings.Secrets[name]
		plan.definition.Hosts = slices.Clone(plan.definition.Hosts)
		plan.definition.OnHosts = cloneSecretStrings(plan.definition.OnHosts)
		plan.use = api.TrustedSecretUseV1{Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound, Bind: binding, Model: *classifier.Model, Endpoint: *classifier.Endpoint}
	}
	scope, _ := json.Marshal(struct {
		Key, Model, Endpoint, Wall, HostID string
		Ceiling                            Tier
	}{name, *classifier.Model, *classifier.Endpoint, wall, hid, ceiling})
	digest := sha256.Sum256(scope)
	plan.scope = hex.EncodeToString(digest[:])
	r := plan.request
	r.mu.Lock()
	before := r.trustedJudge
	if before == nil {
		r.trustedJudge = plan
		before = plan
	}
	r.mu.Unlock()
	if before.conn != c || before.stamp != c.hostTransport || before.operation != operation || before.family != family || before.id != id || before.wall != wall || before.name != name || !sameSandboxCreation(before.creation, creation) || !reflect.DeepEqual(before.classifier, plan.classifier) || !reflect.DeepEqual(before.use, plan.use) {
		return nil, trustedJudgeSourceErrorV1()
	}
	before.mu.Lock()
	samePlan := before.newPlan == p
	before.mu.Unlock()
	if !samePlan {
		before.mu.Lock()
		mayRebind := family == grantOpNew && before.newPlan != nil && p != nil && before.newPlan.creation == nil && p.creation == nil && !before.busy && !before.fetched && !before.closed && reflect.DeepEqual(before.newPlan.layers, p.layers) && slices.Equal(before.newPlan.selection.Names(), p.selection.Names()) && before.newPlan.selection.Kind() == p.selection.Kind()
		if mayRebind {
			before.newPlan = p
		}
		before.mu.Unlock()
		if !mayRebind {
			return nil, trustedJudgeSourceErrorV1()
		}
	}
	if err := before.check(ctx); err != nil {
		return nil, err
	}
	return before, nil
}

func prepareTrustedJudgeNewV1(ctx context.Context, p *newSecretPlan, a NewArgs) error {
	if p == nil {
		return trustedJudgeSourceErrorV1()
	}
	layers, err := retainedNewImagePolicyLayersV1(ctx, p.operation, p.conn.Host, a)
	if err != nil {
		return err
	}
	plan, err := captureTrustedJudgeV1(ctx, p.conn, p.operation, layers, a.Wall, grantOpNew, "", nil, p)
	if err != nil {
		return err
	}
	if plan != nil && plan.name != "" {
		if p.intent == nil {
			return trustedJudgeSourceErrorV1()
		}
		if p.intent.TrustedUses == nil {
			p.intent.TrustedUses = map[string]api.TrustedSecretUseV1{}
		}
		p.intent.TrustedUses[plan.name] = plan.ownedUse()
	}
	return nil
}

func (p *trustedJudgePlanV1) ownedUse() api.TrustedSecretUseV1 {
	out := p.use
	out.Bind.Hosts = slices.Clone(out.Bind.Hosts)
	return out
}

// check retains the same source and only narrows the original ceiling.
// Callers may already hold newPlan.mu; it never acquires that mutex.
func (p *trustedJudgePlanV1) check(ctx context.Context) error {
	if p == nil || ctx == nil || ctx.Err() != nil || grantRequestOf(ctx) != p.request || p.conn == nil || p.conn.hostTransport != p.stamp || p.operation == nil {
		return trustedJudgeSourceErrorV1()
	}
	r := p.request
	r.mu.Lock()
	original := r.trustedJudge == p
	lifetime := r.lifetimeErrorLocked()
	r.mu.Unlock()
	if !original || lifetime != nil {
		return trustedJudgeSourceErrorV1()
	}
	p.mu.Lock()
	closed, ceiling, newPlan := p.closed, p.ceiling, p.newPlan
	p.mu.Unlock()
	if closed {
		return trustedJudgeSourceErrorV1()
	}
	if err := requireGrantOperation(ctx, p.family); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, p.conn); err != nil {
		return err
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return err
	}
	tier, hid, err := grantHostTier(ctx, p.conn.Host)
	if err != nil {
		return err
	}
	if hid != p.conn.HostID() {
		return grantIdentityError(p.conn.Host)
	}
	if p.family == grantOpSecretArm {
		if _, err := ChooseWall(p.conn.Host, tier, p.wall, false); err != nil {
			return err
		}
	}
	tier = min(tier, ceiling, requestCeiling(ctx, tier))
	creation := p.creation
	if newPlan != nil {
		if newPlan.conn != p.conn || newPlan.operation != p.operation {
			return trustedJudgeSourceErrorV1()
		}
		creation = newPlan.creation
	}
	if creation != nil {
		currentTier, saved, err := EffectiveSessionTier(ctx, p.conn.Host, creation.SessionID, hid)
		if err != nil {
			return err
		}
		if !sameSandboxCreation(saved, creation) {
			return trustedJudgeSourceErrorV1()
		}
		tier = min(tier, currentTier, creation.Ceiling)
	}
	if p.name == "" {
		return p.operation.Revalidate(ctx)
	}
	if !p.conn.HasFeature(api.FeatureJudgeKeyV1) || !p.conn.HasFeature(api.FeatureBoundSecrets) || !p.conn.HasFeature("session.id") {
		return p.conn.grantHelperOld("retain the original trusted classifier credential")
	}
	authority, err := trustedJudgeEndpointV1(p.use.Endpoint, p.name)
	if err != nil {
		return err
	}
	if view.Kinds[p.name] != "" || view.Commands[p.name] != p.command || !reflect.DeepEqual(settings.Secrets[p.name], p.definition) || !reflect.DeepEqual(view.Binds[p.name], p.use.Bind) || !slices.Contains(p.use.Bind.Hosts, authority) {
		return trustedJudgeSourceErrorV1()
	}
	p.mu.Lock()
	p.ceiling = min(p.ceiling, tier)
	cold, armNeeded := p.cold, p.armNeeded
	p.mu.Unlock()
	if p.family == grantOpResume && !cold || p.family == grantOpSecretArm && !armNeeded {
		return p.operation.Revalidate(ctx)
	}
	if err := p.allowSecret(ctx, tier, settings); err != nil {
		return err
	}
	minimum, err := minimumSecretTier(settings, p.name)
	if err != nil {
		return err
	}
	if p.wall == "process" && minimum == TierOwn {
		if err := p.allowProcessRisk(ctx, tier); err != nil {
			return err
		}
	}
	return p.operation.Revalidate(ctx)
}
