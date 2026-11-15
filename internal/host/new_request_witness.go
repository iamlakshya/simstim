package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

type newRequestKeyV1 struct{}

// All fields and the issuer stay private to the actual bridge request path.
// Context carries this witness; context values alone cannot issue one.
type newRequestWitnessV1 struct {
	mu        sync.Mutex
	bridge    *bridge
	requestID uint64
	raw       []byte
	encoded   []byte
	need      []byte
	runtime   *selectedRuntimeReceiptV1
	agents    *InstalledAgentsReceiptV1
	captured  bool
	bound     *SessionOperation
	written   bool
}

func newRuntimeError(message string) error {
	return UserError("runtime_origin", message, "Prepare this start on the same authenticated connection with the original client runtime and installed agents receipts.")
}

// newRequestContext is called only by the real decoded bridge New dispatch.
// Inventory and ordinary SDK New calls have no alternative modern issuer.
func (b *bridge) newRequestContext(req proto.Request, args NewArgs) (context.Context, error) {
	if b == nil || b.ctx == nil || req.ID == 0 || req.Op != "new" {
		return nil, newRuntimeError("the new request has no actual authenticated bridge owner")
	}
	if err := errors.Join(CheckCaller(), b.ctx.Err()); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	witness := &newRequestWitnessV1{bridge: b, requestID: req.ID, raw: bytes.Clone(req.Args), encoded: encoded, need: bytes.Clone(args.VMNeedV1)}
	var expected *supply.Manifest
	if len(args.VMNeedV1) != 0 {
		need, err := vmneed.DecodeV1(args.VMNeedV1, runtime.GOARCH)
		if err != nil {
			return nil, err
		}
		witness.agents, err = b.agents.selected(need.Agents)
		if err != nil {
			return nil, err
		}
		expected = &need.Runtime
	}
	if args.Runtime != nil {
		if expected != nil && !reflect.DeepEqual(*expected, *args.Runtime) {
			return nil, newRuntimeError("the new request carries two different runtime selections")
		}
		expected = args.Runtime
	}
	if expected != nil {
		witness.runtime, err = b.runtime.selectedRuntime(*expected)
		if err != nil {
			return nil, err
		}
	}
	return context.WithValue(b.ctx, newRequestKeyV1{}, witness), nil
}

// captureNewRequest owns a deep snapshot before any asynchronous startup work.
// Only the private actual bridge issuer can authorise modern runtime inputs.
func captureNewRequest(ctx context.Context, args NewArgs) (context.Context, NewArgs, error) {
	if ctx == nil {
		return nil, NewArgs{}, newRuntimeError("the new request has no lifetime")
	}
	if err := ctx.Err(); err != nil {
		return nil, NewArgs{}, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, NewArgs{}, err
	}
	var snapshot NewArgs
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return nil, NewArgs{}, err
	}
	modern := len(args.VMNeedV1) != 0 || args.Runtime != nil
	witness, _ := ctx.Value(newRequestKeyV1{}).(*newRequestWitnessV1)
	if witness == nil {
		if modern {
			return nil, NewArgs{}, newRuntimeError("this new request has no same-connection runtime producer")
		}
		return ctx, snapshot, nil
	}
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.bridge == nil || witness.bridge.ctx == nil || witness.bridge.ctx.Err() != nil ||
		witness.requestID == 0 || witness.captured || !bytes.Equal(encoded, witness.encoded) {
		return nil, NewArgs{}, newRuntimeError("the original new request was changed, consumed or ended")
	}
	witness.captured = true
	return ctx, snapshot, nil
}

// admitReservedRuntime binds only that captured original request to its real
// reserved OP. It stages protected data; the final writer retains both native
// producer receipts and the same placement gate through Manifest.Save.
func admitReservedRuntime(ctx context.Context, operation *SessionOperation, m *Manifest, args NewArgs) error {
	if ctx == nil || operation == nil || m == nil || !operation.Valid(m.ID) || operation.native.borrowed || m.path != "" || m.SandboxOf != "" {
		return newRuntimeError("runtime admission has no original reserved primary operation")
	}
	witness, _ := ctx.Value(newRequestKeyV1{}).(*newRequestWitnessV1)
	if len(args.VMNeedV1) == 0 && args.Runtime == nil {
		if m.Wall != nil && (m.Wall.Level == "microvm" || m.Wall.Driver == "gvisor") {
			return newRuntimeError("the selected wall has no original client runtime")
		}
		return nil
	}
	if witness == nil {
		return newRuntimeError("the reserved runtime has no original bridge request")
	}
	witness.mu.Lock()
	defer witness.mu.Unlock()
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if !witness.captured || witness.bound != nil || witness.written || !bytes.Equal(encoded, witness.encoded) ||
		args.ID != operation.Primary() || witness.runtime == nil || witness.bridge.ctx.Err() != nil {
		return newRuntimeError("the new request cannot bind this reserved runtime")
	}
	selected, err := witness.runtime.inventory(ctx)
	if err != nil {
		return err
	}
	if len(witness.need) != 0 {
		need, err := vmneed.DecodeV1(witness.need, runtime.GOARCH)
		if err != nil {
			return err
		}
		if m.Wall == nil || need.ValidateWall(runtime.GOARCH, m.Wall.Level, m.Wall.Driver, m.Limits.MemoryMax) != nil ||
			!reflect.DeepEqual(need.Runtime, selected) || witness.agents == nil || sessionMemoryMax(m.Limits.MemoryMax) != m.Limits.MemoryMax {
			return newRuntimeError("the original VM Need conflicts with the selected wall, runtime or admitted cap")
		}
		disk, err := witness.agents.Inventory()
		if err != nil || disk.Content != need.Agents {
			return errors.Join(newRuntimeError("the original VM Need lost its selected installed agents receipt"), err)
		}
		m.VMNeedV1, m.Agents = bytes.Clone(witness.need), need.Agents.SHA256
	} else {
		if m.Wall == nil || m.Wall.Level != "gvisor" || m.Wall.Driver != "gvisor" || args.Runtime == nil || !reflect.DeepEqual(*args.Runtime, selected) {
			return newRuntimeError("the original runtime conflicts with its selected gvisor wall")
		}
		if _, err := gvisor.RuntimeItemsV1(selected, runtime.GOARCH); err != nil {
			return err
		}
	}
	m.RuntimeManifest = &selected
	witness.bound, operation.newRequest = operation, witness
	return nil
}

func saveReservedManifest(operation *SessionOperation, m *Manifest) error {
	if operation == nil || m == nil || !operation.Valid(m.ID) {
		return newRuntimeError("the reserved writer lost its original primary operation")
	}
	witness := operation.newRequest
	if witness == nil {
		if len(m.VMNeedV1) != 0 || m.RuntimeManifest != nil {
			return newRuntimeError("a raw manifest cannot issue runtime authority")
		}
		return operation.saveNewManifest(m)
	}
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.bound != operation || witness.written || !bytes.Equal(witness.need, m.VMNeedV1) {
		return newRuntimeError("the reserved runtime writer is no longer the original one-use request")
	}
	witness.runtime.mu.RLock()
	defer witness.runtime.mu.RUnlock()
	if witness.agents != nil {
		witness.agents.mu.RLock()
		defer witness.agents.mu.RUnlock()
	}
	if err := witness.runtime.checkLocked(operation.ownerContext); err != nil {
		return err
	}
	if m.RuntimeManifest == nil || !reflect.DeepEqual(witness.runtime.manifest, *m.RuntimeManifest) {
		return newRuntimeError("the protected runtime changed after original admission")
	}
	// One real native placement gate covers both exact active producers and
	// the final durable writer. Never nest their independent gate callbacks.
	err := withPlacement(operation.ownerContext, false, func(control *placementControl) error {
		if err := witness.checkProducersLocked(control); err != nil {
			return err
		}
		if err := operation.native.check(operation.ownerContext); err != nil {
			return err
		}
		return operation.saveNewManifest(m)
	}, false)
	if err == nil {
		witness.written = true
	}
	return err
}

func (witness *newRequestWitnessV1) checkProducersLocked(control *placementControl) error {
	check := func(publication *runtimePublication) error {
		if publication == nil || publication.operation == nil || publication.owner == nil || publication.ctx.Err() != nil {
			return newRuntimeError("an original runtime publication ended before the protected save")
		}
		if current, ok := control.Active[publication.operation.token]; !ok || current != publication.operation.record {
			return newRuntimeError("the original runtime publication lost its active native reservation")
		}
		return publication.owner.checkIdentity()
	}
	if witness.bridge == nil || witness.bridge.ctx.Err() != nil || witness.runtime.closed {
		return newRuntimeError("the original authenticated connection ended before the protected save")
	}
	if err := check(witness.runtime.publication); err != nil {
		return err
	}
	if witness.agents != nil {
		if witness.agents.closed || witness.agents.lease == nil || witness.agents.store == nil ||
			witness.agents.publication == nil || witness.agents.publication.owner != witness.runtime.publication.owner {
			return newRuntimeError("the installed agents receipt no longer shares the original host owner")
		}
		if err := check(witness.agents.publication); err != nil {
			return err
		}
		var disk image.AgentDiskV1 = witness.agents.disk
		if err := disk.Validate(runtime.GOARCH); err != nil {
			return err
		}
	}
	return nil
}
