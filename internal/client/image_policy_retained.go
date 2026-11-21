package client

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// This capsule retains the actual local descriptor and accepted connection.
// Its policy copy is input to verification, never a transport or boot grant.
type imagePolicyUseV1 struct {
	request   *GrantRequest
	family    grantOperation
	layers    []api.Layer
	conn      *Conn
	stamp     *hostTransportStamp
	source    hostStateSource
	hostID    string
	operation *settingsOperation
	policy    *api.ImagePolicyValues
}
type imagePolicyUseKeyV1 struct{}

func cloneImagePolicyValuesV1(p *api.ImagePolicyValues) *api.ImagePolicyValues {
	if p == nil {
		return nil
	}
	out := *p
	if p.Identity != nil {
		v := *p.Identity
		out.Identity = &v
	}
	if p.Issuer != nil {
		v := *p.Issuer
		out.Issuer = &v
	}
	if p.TrustedRoot != nil {
		v := *p.TrustedRoot
		out.TrustedRoot = &v
	}
	return &out
}

func imagePolicyValuesForLayersV1(ctx context.Context, operation *settingsOperation, layers []api.Layer) (*api.ImagePolicyValues, error) {
	policy, _, err := egress.MergeLayers(api.CloneLayers(layers))
	if err != nil {
		return nil, policyError(err)
	}
	value := cloneImagePolicyValuesV1(policy.Sandbox.ImagePolicy)
	snapshot, err := operation.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if value != nil && value.TrustedRoot != nil && *value.TrustedRoot != "" && !filepath.IsAbs(*value.TrustedRoot) {
		// The original descriptor's literal config directory owns relative roots.
		root := filepath.Join(filepath.Dir(snapshot.path), *value.TrustedRoot)
		if !filepath.IsAbs(root) {
			return nil, grantOperationError()
		}
		value.TrustedRoot = &root
	}
	return value, operation.Revalidate(ctx)
}

func (p *imagePolicyUseV1) check(ctx context.Context, c *Conn) error {
	if p == nil || c == nil || c.hostTransport == nil || p.conn != c || p.stamp != c.hostTransport || p.hostID != c.HostID() || !sameHostTransportSource(p.source, c.hostTransport.source) {
		return grantOperationError()
	}
	if p.request != nil {
		if p.request != grantRequestOf(ctx) {
			return grantOperationError()
		}
		if p.family == grantOpSandboxPrepare {
			operation, err := sandboxPrepareImagePhaseV1(ctx, c)
			if err != nil {
				return err
			}
			if operation != p.operation {
				return grantOperationError()
			}
		} else if err := requireGrantOperation(ctx, p.family); err != nil {
			return err
		}
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return err
	}
	metadata, err := newOrdinaryLocalGitMetadataSource(ctx, p.operation)
	if err != nil {
		return err
	}
	// These are fixed native verifier/config/cache lookup keys, not value names.
	err = metadata.CheckKeys(ctx, []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "GOMAXPROCS", "GOMEMLIMIT", "DOCKER_CONFIG", "PATH"})
	return errors.Join(err, metadata.Close())
}

// captureNewImagePolicy freezes the effective original merged New policy.
// Retries cannot select a different policy, descriptor or accepted connection.
func captureNewImagePolicy(ctx context.Context, c *Conn, a NewArgs) (context.Context, error) {
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return ctx, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return ctx, err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return ctx, grantOperationError()
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return ctx, err
	}
	layers, err := retainedNewImagePolicyLayersV1(ctx, operation, c.Host, a)
	if err != nil {
		return ctx, err
	}
	values, err := imagePolicyValuesForLayersV1(ctx, operation, layers)
	if err != nil {
		return ctx, err
	}
	candidate := &imagePolicyUseV1{request: r, family: grantOpNew, layers: api.CloneLayers(layers), conn: c, stamp: c.hostTransport, source: c.hostTransport.source, hostID: c.HostID(), operation: operation, policy: values}
	if err := candidate.check(ctx, c); err != nil {
		return ctx, err
	}
	r.mu.Lock()
	before := r.imagePolicy
	if before == nil {
		r.imagePolicy = candidate
		before = candidate
	}
	r.mu.Unlock()
	if before.conn != c || before.stamp != candidate.stamp || before.operation != operation || !reflect.DeepEqual(before.policy, values) {
		return ctx, grantOperationError()
	}
	if err := before.check(ctx, c); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, imagePolicyUseKeyV1{}, before), nil
}

// withRetainedImagePolicy carries the SAME descriptor to lower registry,
// verifier and pin boundaries. Nested borrows never close the original owner.
func withRetainedImagePolicy(ctx context.Context, c *Conn) (context.Context, *api.ImagePolicyValues, func() error, error) {
	if r := grantRequestOf(ctx); r != nil && requestGrantPurpose(r) == grantHostPrepare {
		owned, err := captureSandboxPrepareImagePolicyV1(ctx, c)
		if err != nil {
			return ctx, nil, nil, err
		}
		p, _ := owned.Value(imagePolicyUseKeyV1{}).(*imagePolicyUseV1)
		return owned, nil, func() error { return p.check(owned, c) }, nil
	}
	if p, _ := ctx.Value(imagePolicyUseKeyV1{}).(*imagePolicyUseV1); p != nil {
		if err := p.check(ctx, c); err != nil {
			return ctx, nil, nil, err
		}
		return ctx, cloneImagePolicyValuesV1(p.policy), func() error { return p.check(ctx, c) }, nil
	}
	if r := grantRequestOf(ctx); r != nil && (requestGrantOperation(r) == grantOpNew || requestGrantOperation(r) == grantOpResume) {
		r.mu.Lock()
		p := r.imagePolicy
		r.mu.Unlock()
		if p == nil {
			return ctx, nil, nil, grantOperationError()
		}
		owned := context.WithValue(ctx, imagePolicyUseKeyV1{}, p)
		if err := p.check(owned, c); err != nil {
			return ctx, nil, nil, err
		}
		return owned, cloneImagePolicyValuesV1(p.policy), func() error { return p.check(owned, c) }, nil
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return ctx, nil, nil, err
	}
	operation, err := openSettingsOperation(ctx)
	if err != nil {
		return ctx, nil, nil, err
	}
	fail := func(err error) (context.Context, *api.ImagePolicyValues, func() error, error) {
		return ctx, nil, nil, errors.Join(err, operation.Close())
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return fail(err)
	}
	if err := ValidateSettings(settings); err != nil {
		return fail(err)
	}
	host := cloneGrantHost(c.Host)
	layers := []api.Layer{{Kind: api.LayerAll, LayerValues: settings.LayerValues, Lock: policyLock(settings.Lock)},
		{Kind: api.LayerHost, Name: host.Name, LayerValues: host.LayerValues, Lock: policyLock(host.Lock)}}
	values, err := imagePolicyValuesForLayersV1(ctx, operation, layers)
	if err != nil {
		return fail(err)
	}
	p := &imagePolicyUseV1{conn: c, stamp: c.hostTransport, source: c.hostTransport.source, hostID: c.HostID(), operation: operation, policy: values}
	if err := p.check(ctx, c); err != nil {
		return fail(err)
	}
	owned := context.WithValue(ctx, imagePolicyUseKeyV1{}, p)
	return owned, cloneImagePolicyValuesV1(values), func() error { return errors.Join(p.check(owned, c), operation.Close()) }, nil
}

func retainedImagePolicy(ctx context.Context, c *Conn) (*api.ImagePolicyValues, func() error, error) {
	_, values, close, err := withRetainedImagePolicy(ctx, c)
	return values, close, err
}
func checkRetainedImagePolicy(ctx context.Context, c *Conn) error {
	p, _ := ctx.Value(imagePolicyUseKeyV1{}).(*imagePolicyUseV1)
	if p == nil {
		return grantOperationError()
	}
	return p.check(ctx, c)
}
func revalidateRetainedImagePolicy(ctx context.Context) error {
	p, _ := ctx.Value(imagePolicyUseKeyV1{}).(*imagePolicyUseV1)
	if p == nil {
		return grantOperationError()
	}
	return p.check(ctx, p.conn)
}

// Always materialise all/host from the original descriptor and accepted host.
// A direct SDK caller cannot omit a broader certificate policy by omitting its
// layers. Named repository/profile identities still use canonical refresh.
func retainedNewImagePolicyLayersV1(ctx context.Context, operation *settingsOperation, h Host, a NewArgs) ([]api.Layer, error) {
	settings, err := operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if len(a.Layers) == 0 {
		return ResolvePolicyLayers(settings, h, "", "", api.LayerValues{})
	}
	layers, err := refreshPolicyLayers(settings, h, a.Layers)
	if err != nil {
		return nil, err
	}
	base := []api.Layer{{Kind: api.LayerAll, LayerValues: settings.LayerValues, Lock: policyLock(settings.Lock)},
		{Kind: api.LayerHost, Name: h.Name, LayerValues: h.LayerValues, Lock: policyLock(h.Lock)}}
	for _, layer := range layers {
		if layer.Kind == api.LayerAll {
			continue
		}
		if layer.Kind == api.LayerHost {
			if layer.Name != h.Name {
				return nil, grantOperationError()
			}
			continue
		}
		base = append(base, layer)
	}
	if a.RepositoryPolicy != nil {
		base = append(base, api.CloneLayers([]api.Layer{a.RepositoryPolicy.Layer})[0])
	}
	return base, nil
}
