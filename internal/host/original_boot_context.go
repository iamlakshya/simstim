package host

import (
	"bytes"
	"context"
	"errors"
	"net"
	"reflect"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

type originalBootContextKeyV1 struct{}

// OriginalVMNeed is the host-owned original protected boot receipt. Its raw
// Need and native supervisor/loan are private. A spec, row, copied identifier,
// environment descriptor number or caller callback cannot construct one.
type OriginalVMNeed struct {
	boot             *originalBootV1
	need             *vmneed.NeedV1
	spec             wall.Spec
	rawNeed          []byte
	prepareMu        sync.Mutex
	prepareUsed      bool
	prepareConfirmed bool
	correlation      proto.VMPrepareCorrelationV1
	binding          *microvm.NativeBindingV1
	connection       net.Conn
}

// VMRunContextLocked freshly loads the protected primary inside its actual
// owned/borrowed OP and native boot owner. It never accepts a raw Manifest.
func VMRunContextLocked(ctx context.Context, operation *SessionOperation, owner *OwnedSupervisorLease) (context.Context, error) {
	if ctx == nil || owner == nil || owner.native == nil || operation == nil || !owner.Valid(operation.Primary()) || !operation.Valid(operation.Primary()) {
		return nil, vmNeedError("this boot has no current original operation and native supervisor owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	if boot == nil || boot.owner != owner || boot.operation != operation || operation.originalBoot != boot || boot.line == nil {
		return nil, vmNeedError("this boot has no original outside-issued native operation loan")
	}
	m, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	runtimeBytes, err := startupRuntimeBytes(m)
	if err != nil || m.path != "" || m.SandboxOf != "" || m.Wall == nil || !bytes.Equal(m.VMNeedV1, boot.currentNeed) ||
		startupDigest(boot.need) != boot.loan.Need || startupDigest(runtimeBytes) != boot.loan.Runtime ||
		!reflect.DeepEqual(m.RuntimeManifest, boot.runtime) {
		return nil, errors.Join(vmNeedError("the original protected boot source changed during native handoff"), err)
	}
	if sessionMemoryMax(m.Limits.MemoryMax) != m.Limits.MemoryMax {
		return nil, vmNeedError("the recorded unit cap is no longer admitted")
	}
	if err := ValidateManifestStart(m); err != nil {
		return nil, err
	}
	receipt := &OriginalVMNeed{boot: boot, rawNeed: bytes.Clone(boot.currentNeed)}
	plan, err := SandboxStatePlan(m)
	if err != nil {
		return nil, err
	}
	spec := wall.Spec{ID: m.ID, SessionDir: paths.SessionDir(m.ID), MemoryMax: m.Limits.MemoryMax,
		StateDirs: plan.Dirs, StateFiles: plan.Files}
	if boot.runtime != nil {
		copy := cloneSelectedRuntime(*boot.runtime)
		spec.RuntimeManifest = &copy
	}
	if len(receipt.rawNeed) != 0 {
		need, err := vmneed.DecodeV1(receipt.rawNeed, runtime.GOARCH)
		if err != nil {
			return nil, err
		}
		if err := need.ValidateWall(runtime.GOARCH, m.Wall.Level, m.Wall.Driver, m.Limits.MemoryMax); err != nil {
			return nil, err
		}
		if boot.runtime == nil || !reflect.DeepEqual(need.Runtime, *boot.runtime) {
			return nil, vmNeedError("the original VM Need lost its own held runtime selection")
		}
		copy := need.BootV1.Clone()
		spec.CPUs, spec.DiskMax, spec.VM, receipt.need = need.CPUs, need.DiskMax, &copy, need
	} else if m.Wall.Level == "microvm" || m.Wall.Driver == "chv" || m.Wall.Driver == "firecracker" {
		return nil, vmNeedError("this actual microvm boot has no original authenticated Need")
	}
	if m.Wall.Driver == "gvisor" && (spec.Image != nil || spec.VM != nil || spec.RuntimeManifest == nil) {
		return nil, vmNeedError("the original gvisor runtime has conflicting VM or image boot data")
	}
	receipt.spec = spec
	// ExpectedSpec remains a data projection. Native runtime consumers must
	// also consume this private receipt and bind their actual owned runtime.
	ctx = context.WithValue(ctx, originalBootContextKeyV1{}, receipt)
	return wall.WithExpectedSpec(ctx, spec), nil
}

// OriginalVMNeedFromContext returns only a receipt issued from the same native
// supervisor's accepted outside loan. It never synthesises one from a spec.
func OriginalVMNeedFromContext(ctx context.Context) *OriginalVMNeed {
	if ctx == nil {
		return nil
	}
	receipt, _ := ctx.Value(originalBootContextKeyV1{}).(*OriginalVMNeed)
	if receipt == nil || receipt.boot == nil || receipt.boot.owner == nil || receipt.boot.owner.native == nil ||
		!receipt.boot.owner.Valid(receipt.boot.loan.Primary) || ctx.Err() != nil {
		return nil
	}
	receipt.boot.owner.mu.Lock()
	defer receipt.boot.owner.mu.Unlock()
	if !bytes.Equal(receipt.rawNeed, receipt.boot.currentNeed) {
		return nil
	}
	return receipt
}

// ExpectedOriginalRuntime projects only the immutable original runtime from
// the accepted operation/boot owner. Inventory and a supplied Spec cannot issue
// this projection. The registered driver still checks every held runtime role.
func ExpectedOriginalRuntime(ctx context.Context) (supply.Manifest, error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil || receipt.boot.runtime == nil {
		return supply.Manifest{}, vmNeedError("the runtime has no original native boot-owner selection")
	}
	return cloneSelectedRuntime(*receipt.boot.runtime), nil
}
