package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

// AgentsRefreshV1 changes only the two agent-image retention fields during
// the original received automatic boot. It retains the actual producer and
// native owner; a saved row, ContentRef or callback flag cannot create one.
type AgentsRefreshV1 struct {
	active      atomic.Bool
	applied     atomic.Bool
	committed   atomic.Bool
	id          string
	beforeNeed  json.RawMessage
	beforeDisk  string
	afterNeed   json.RawMessage
	afterDisk   string
	original    *OriginalVMNeed
	retained    *InstalledAgentsReceiptV1
	selected    image.AgentDiskV1
	publication context.Context
	expected    context.Context
}

// PrepareAutomaticAgentsV1 consumes the SAME outside-issued automatic loan
// once. Conversion runs outside placement/store locks. Its real receipt and
// publication gate remain held through the canonical strong writer callback.
func PrepareAutomaticAgentsV1(ctx context.Context, authority *AutomaticBootOperationV1, publish func(*AgentsRefreshV1) error) (retained *InstalledAgentsReceiptV1, returned error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil || receipt.need == nil || authority == nil || authority.operation != receipt.boot.operation ||
		receipt.boot.loan.Purpose != "automatic" || publish == nil {
		return nil, automaticRestoreError()
	}
	defer func() {
		if returned != nil && retained != nil {
			returned = errors.Join(returned, retained.Close())
			retained = nil
		}
	}()
	returned = authority.WithCurrent(ctx, func(current *Manifest) error {
		if !bytes.Equal(current.VMNeedV1, receipt.rawNeed) || current.ID != receipt.boot.loan.Primary || current.Agents != "" && current.Agents != receipt.need.Agents.SHA256 {
			return automaticRestoreError()
		}
		receipt.prepareMu.Lock()
		alreadyBound := receipt.prepareUsed || receipt.binding != nil
		receipt.prepareMu.Unlock()
		if alreadyBound {
			return vmNeedError("the automatic agents image cannot change after native VM construction or prepare")
		}
		publication, err := beginAutomaticAgentsPublication(ctx, receipt.boot)
		if err != nil {
			return err
		}
		retained, err = ensureInstalledAgentsPublicationV1(publication)
		if err != nil {
			return err
		}
		disk, err := retained.Inventory()
		if err != nil {
			return err
		}
		edit, err := automaticAgentsEditV1(ctx, receipt, current, disk, retained)
		if err != nil {
			return err
		}
		return retained.WithPublicationV1(ctx, func(selected image.AgentDiskV1) error {
			if selected.Content != disk.Content {
				return agentsBuildFailure("the original automatic agents producer changed its selected content")
			}
			edit.active.Store(true)
			defer edit.active.Store(false)
			return publish(edit)
		})
	})
	return retained, returned
}

func automaticAgentsEditV1(ctx context.Context, original *OriginalVMNeed, current *Manifest, disk image.AgentDiskV1, retained *InstalledAgentsReceiptV1) (*AgentsRefreshV1, error) {
	if err := disk.Validate(runtime.GOARCH); err != nil {
		return nil, err
	}
	need, err := vmneed.DecodeV1(current.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if original == nil || original.boot == nil || original.need == nil || !reflect.DeepEqual(*need, *original.need) {
		return nil, automaticRestoreError()
	}
	need.Agents = disk.Content
	raw, err := vmneed.EncodeV1(*need, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	spec := original.spec
	boot := need.BootV1.Clone()
	spec.VM = &boot
	future := &OriginalVMNeed{boot: original.boot, need: need, spec: spec, rawNeed: bytes.Clone(raw)}
	// This future projection refuses until CommitProtectedV1 has verified
	// the real durable row and advanced this same private native boot owner.
	expected := context.WithValue(wall.WithExpectedSpec(ctx, spec), originalBootContextKeyV1{}, future)
	return &AgentsRefreshV1{id: current.ID, beforeNeed: bytes.Clone(current.VMNeedV1), beforeDisk: current.Agents,
		afterNeed: raw, afterDisk: disk.Content.SHA256, original: original, retained: retained, selected: disk, publication: ctx, expected: expected}, nil
}

// PublicationContextV1 carries only this actual active producer's writer
// lifetime. The future boot context is available only after the strong save.
func (edit *AgentsRefreshV1) PublicationContextV1() (context.Context, error) {
	if edit == nil || !edit.active.Load() || edit.publication == nil || edit.publication.Err() != nil {
		return nil, automaticRestoreError()
	}
	return edit.publication, nil
}

// ApplyV1 edits the prospective protected primary once, inside the actual
// retained producer callback. It never edits a guest/member or a stale row.
func (edit *AgentsRefreshV1) ApplyV1(target *Manifest) error {
	if edit == nil || !edit.active.Load() || edit.original == nil || edit.retained == nil || target == nil || target.path != "" || target.SandboxOf != "" ||
		target.ID != edit.id || target.Agents != edit.beforeDisk || !bytes.Equal(target.VMNeedV1, edit.beforeNeed) ||
		OriginalVMNeedFromContext(edit.publication) != edit.original || !edit.applied.CompareAndSwap(false, true) {
		return automaticRestoreError()
	}
	target.Agents, target.VMNeedV1 = edit.afterDisk, bytes.Clone(edit.afterNeed)
	return nil
}

// CommitProtectedV1 runs after the actual strong save, while the producer
// callback still owns its gate and receipt. It rereads the exact original OP;
// no prospective Manifest or caller success boolean can advance authority.
func (edit *AgentsRefreshV1) CommitProtectedV1(ctx context.Context) error {
	if edit == nil || !edit.active.Load() || !edit.applied.Load() || edit.committed.Load() || edit.original == nil ||
		edit.retained == nil || edit.retained.publication == nil || edit.retained.publication.ctx.Err() != nil || ctx == nil {
		return automaticRestoreError()
	}
	owner := edit.original.boot.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	operation := edit.original.boot.operation
	if owner.startup != edit.original.boot || !owner.Valid(edit.id) || !operation.Valid(edit.id) ||
		!bytes.Equal(owner.startup.currentNeed, edit.beforeNeed) || edit.selected.Content.SHA256 != edit.afterDisk {
		return automaticRestoreError()
	}
	saved, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if saved.ID != edit.id || saved.Agents != edit.afterDisk || !bytes.Equal(saved.VMNeedV1, edit.afterNeed) ||
		!reflect.DeepEqual(saved.RuntimeManifest, owner.startup.runtime) {
		return automaticRestoreError()
	}
	need, err := vmneed.DecodeV1(saved.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return err
	}
	if need.Agents != edit.selected.Content || need.ValidateWall(runtime.GOARCH, wallLevel(saved), wallDriver(saved), saved.Limits.MemoryMax) != nil {
		return automaticRestoreError()
	}
	original := edit.original.need.Clone()
	current := need.Clone()
	current.Agents = original.Agents
	if !reflect.DeepEqual(original, current) {
		return automaticRestoreError()
	}
	owner.startup.currentNeed = bytes.Clone(edit.afterNeed)
	edit.committed.Store(true)
	return nil
}

// ExpectedContextV1 returns the one committed agents-only projection from
// the same native owner after its actual protected row has been reread.
func (edit *AgentsRefreshV1) ExpectedContextV1() (context.Context, error) {
	if edit == nil || !edit.active.Load() || !edit.committed.Load() || edit.expected == nil || OriginalVMNeedFromContext(edit.expected) == nil {
		return nil, automaticRestoreError()
	}
	return edit.expected, nil
}
