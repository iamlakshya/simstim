package client

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

type resumeVMPreparationKeyV1 struct{}

// Only the original cold preparation lends the existing signed-runtime and
// agents producers its Resume purpose. A copied Need cannot enter this scope.
func requireResumeVMProducerV1(ctx context.Context, c *Conn) error {
	if ctx == nil {
		return grantOperationError()
	}
	n, _ := ctx.Value(resumeVMPreparationKeyV1{}).(*resumeNamedSSHKeyPlan)
	if n == nil || n.conn != c || n.request != grantRequestOf(ctx) {
		return grantOperationError()
	}
	if err := n.check(ctx, c, false); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.prepared == nil || n.prepared.Mode != "cold" || n.prepared.VM == nil || n.prepared.Prepared == nil || n.closed || n.closing {
		return grantOperationError()
	}
	return nil
}

func resolveResumeVMOptionsV1(a ResumeArgs, layers []api.Layer, kind string, prior *api.ResumeVMSelectionV1) (VMNeedOptionsV1, error) {
	if prior == nil || prior.V != 1 {
		return VMNeedOptionsV1{}, vmPrepareUnavailable()
	}
	// The architecture is checked again by the actual host probe and Need builder.
	var shape struct {
		Image struct {
			Arch string `json:"arch"`
		} `json:"image"`
	}
	if err := json.Unmarshal(prior.Need, &shape); err != nil {
		return VMNeedOptionsV1{}, resumeVMSizeErrorV1(err)
	}
	old, err := vmneed.DecodeV1(prior.Need, shape.Image.Arch)
	if err != nil {
		return VMNeedOptionsV1{}, resumeVMSizeErrorV1(err)
	}
	policy, _, err := egress.MergeLayers(layers)
	if err != nil {
		return VMNeedOptionsV1{}, policyError(err)
	}
	ram := old.GuestRAM
	if a.MemoryMax > 0 {
		ram = a.MemoryMax
	} else if a.MemoryMax == api.MemoryAuto || !reflect.DeepEqual(prior.PolicyMemory, policy.Sandbox.Memory) {
		var defaults *api.SandboxSize
		if agent, known := agents.Find(kind); known {
			defaults = agent.SandboxSize()
		}
		size, err := api.ResolveSandboxSize(policy.Sandbox, nil, defaults)
		if err != nil {
			return VMNeedOptionsV1{}, resumeVMSizeErrorV1(err)
		}
		ram = size.Memory
	}
	if _, err := vmneed.BudgetFor(ram); err != nil {
		return VMNeedOptionsV1{}, &UserError{Msg: CleanLine(err.Error()), Hint: "Choose a positive guest memory size in whole MiB.", Code: "memory_max", Kind: KindUsage}
	}
	image := "default"
	if policy.Sandbox.Image != nil {
		image = *policy.Sandbox.Image
	}
	return VMNeedOptionsV1{Arch: old.Image.Arch, Image: image, Intent: "microvm", Driver: old.Driver, GuestRAM: ram, CPUs: old.CPUs, DiskMax: old.DiskMax}, nil
}

func (n *resumeNamedSSHKeyPlan) prepareVM(ctx context.Context, c *Conn, a ResumeArgs, prior *api.ResumePrepareResultV1) (*api.ResumePrepareResultV1, error) {
	if prior == nil || prior.VM == nil || !c.HasFeature(api.FeatureResumeVMSizeV1) {
		return nil, namedSSHKeySourceError()
	}
	owned := context.WithValue(resumePreparationRPCContextV1(ctx), resumeVMPreparationKeyV1{}, n)
	if err := requireResumeVMProducerV1(owned, c); err != nil {
		return nil, err
	}
	kind := ""
	if n.creation.lineage != nil {
		kind = n.creation.lineage.SelectedKind
	}
	options, err := resolveResumeVMOptionsV1(a, n.layers, kind, prior.VM)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	plan := n.vm
	n.mu.Unlock()
	if plan == nil {
		plan, err = c.prepareVMNeedV1(owned, options, n.request)
		if err != nil {
			return nil, err
		}
		// The retained creation image is the original expected-image projection.
		encoded, err := vmneed.EncodeImageV1(plan.Need().Image)
		if err != nil || !bytes.Equal(encoded, a.ExpectedImageV1) {
			return nil, imageSelectionHistoryError()
		}
		n.mu.Lock()
		n.vm = plan
		n.mu.Unlock()
	}
	raw, err := plan.EncodedFor(c)
	if err != nil {
		return nil, err
	}
	if len(a.VMNeedV1) != 0 && !bytes.Equal(a.VMNeedV1, raw) {
		return nil, namedSSHKeySourceError()
	}
	a, err = n.vmArguments(a)
	if err != nil {
		return nil, err
	}
	metadata, err := n.metadataBytes(a)
	if err != nil {
		return nil, err
	}
	var start ResumeArgs
	if err := json.Unmarshal(metadata, &start); err != nil {
		return nil, err
	}
	origin, err := plan.runtimeOriginForV1(owned, c)
	if err != nil {
		return nil, err
	}
	var result api.ResumePrepareResultV1
	if err := c.Call(resumePreparationRPCContextV1(owned), "resume.vm.prepare-v1", api.ResumeVMPrepareArgsV1{V: 1, RuntimeOrigin: origin, Prepared: api.ClonePreparedResumeV1(prior.Prepared), Start: start}, &result); err != nil {
		return nil, err
	}
	if !result.Valid() || result.Mode != "cold" || result.VM != nil || result.Primary != prior.Primary || !reflect.DeepEqual(result.Prepared, prior.Prepared) {
		return nil, namedSSHKeySourceError()
	}
	n.mu.Lock()
	n.prepared = &result
	n.metadata = bytes.Clone(metadata)
	n.mu.Unlock()
	return &result, nil
}

func (n *resumeNamedSSHKeyPlan) vmArguments(a ResumeArgs) (ResumeArgs, error) {
	n.mu.Lock()
	plan := n.vm
	n.mu.Unlock()
	if plan == nil {
		if len(a.VMNeedV1) != 0 {
			return a, namedSSHKeySourceError()
		}
		return a, nil
	}
	raw, err := plan.EncodedFor(n.conn)
	if err != nil {
		return a, err
	}
	if len(a.VMNeedV1) != 0 && !bytes.Equal(a.VMNeedV1, raw) {
		return a, namedSSHKeySourceError()
	}

	a.VMNeedV1 = raw
	return a, nil
}

func resumeVMSizeErrorV1(err error) error {
	return &UserError{Msg: "the retained resume VM size is invalid: " + CleanLine(err.Error()), Hint: "Check the saved sandbox and current memory policy, then prepare its resume again.", Code: "vm_expected", Kind: KindRefused}
}

// A fetch confirmation outlives one joined preparation call. Its retained
// work follows the original Resume lifetime; each active transfer separately
// follows the current caller's cancellation and is joined before it returns.
type resumeVMImageFetchContextV1 struct {
	context.Context
	values context.Context
}

func (c resumeVMImageFetchContextV1) Value(key any) any { return c.values.Value(key) }
func resumeVMImageFetchLifetimeV1(ctx context.Context, c *Conn) (context.Context, error) {
	if err := requireResumeVMProducerV1(ctx, c); err != nil {
		return nil, err
	}
	n, _ := ctx.Value(resumeVMPreparationKeyV1{}).(*resumeNamedSSHKeyPlan)
	if n == nil || n.lifetime == nil || n.lifetime.Err() != nil {
		return nil, namedSSHKeySourceError()
	}
	return resumeVMImageFetchContextV1{Context: n.lifetime, values: ctx}, nil
}
