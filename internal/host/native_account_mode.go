package host

import (
	"bytes"
	"context"
	"net"
	"os"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// NativeAccountOwnV1 names descriptive data from an original native slot run.
const NativeAccountOwnV1 = "own-account"

// OriginalAccountModeV1 observes only this same original prepared VM, while
// its pending native owner and OP still guard the actual ready publication.
// A result is descriptive data and creates no source or execution permission.
func (receipt *OriginalVMNeed) OriginalAccountModeV1(ctx context.Context, runtime wall.Sandbox, connection net.Conn) (bool, error) {
	if ctx == nil {
		return false, vmNeedError("the account observation has no original owner context")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || receipt.need == nil || runtime == nil || connection == nil {
		return false, vmNeedError("the account observation has no original VM operation")
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	if !receipt.prepareConfirmed || receipt.binding == nil || receipt.connection != connection {
		return false, vmNeedError("the account observation has another prepared VM connection")
	}
	owner := receipt.boot.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup != receipt.boot || owner.pending == nil || receipt.boot.ready || !owner.Valid(receipt.boot.loan.Primary) || !receipt.boot.operation.Valid(receipt.boot.loan.Primary) {
		return false, vmNeedError("the original account observation is outside its pending ready publication")
	}
	if err := owner.pending.peer.Check(); err != nil {
		return false, err
	}
	saved, err := receipt.boot.operation.LoadContext(ctx)
	if err != nil {
		return false, err
	}
	if saved.Life == nil || saved.Life.Phase != LifeRunning || saved.Life.Started.IsZero() || !saved.Life.Started.Equal(saved.Run.StartedAt) || saved.Run.Generation != owner.generation || saved.Run.HolderPID != os.Getpid() || saved.Run.BootID != BootID() ||
		!bytes.Equal(saved.VMNeedV1, receipt.rawNeed) || !bytes.Equal(receipt.boot.currentNeed, receipt.rawNeed) || !reflect.DeepEqual(saved.RuntimeManifest, receipt.boot.runtime) || receipt.binding.SessionID() != saved.ID {
		return false, vmNeedError("the original native account observation conflicts with its protected ready run")
	}
	return microvm.OriginalAccountModeV1(ctx, runtime, connection, receipt.binding, *receipt.need)
}

// nativeAccountActiveV1 is a row projection of protected run data. Actual
// concrete native ready issuance is the only production writer of the mark.
// Unknown, retired, old-boot or mismatched run data supplies no active label.
func nativeAccountActiveV1(manifest *Manifest, alive bool) bool {
	if !alive || manifest == nil || manifest.Wall == nil || manifest.Wall.Level != "microvm" || manifest.Wall.Driver != "chv" && manifest.Wall.Driver != "firecracker" && manifest.Wall.Driver != "nitro" || manifest.Life == nil || manifest.Life.Phase != LifeRunning || manifest.Life.Started.IsZero() || !manifest.Life.Started.Equal(manifest.Run.StartedAt) || manifest.Run.BootID != BootID() || manifest.Run.Generation == "" || manifest.Run.StartedAt.IsZero() {
		return false
	}
	if manifest.SandboxOf != "" {
		if !api.ValidSessionID(manifest.SandboxOf) || manifest.SandboxOf == manifest.ID {
			return false
		}
		root, err := LoadManifest(manifest.SandboxOf)
		if err != nil || root.ID != manifest.SandboxOf || root.SandboxOf != "" || root.Wall == nil || root.Wall.Driver != manifest.Wall.Driver || !publicationRunCoherent(manifest, []*Manifest{root}) {
			return false
		}
		return nativeAccountActiveV1(root, alive)
	}
	return manifest.Run.NativeAccountMode == NativeAccountOwnV1 && manifest.Run.NativeAccountGeneration == manifest.Run.Generation
}
