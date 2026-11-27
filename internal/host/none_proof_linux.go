//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/none"
	"golang.org/x/sys/unix"
)

// This is a distinct live NONE proof, not a relaxation of the managed whole-
// stop issuer. The actual original creator binding supplies its nil VM role.
func (owner *OwnedSupervisorLease) WriteNativeNoneWarmProofV1(ctx context.Context, connection net.Conn, response proto.Response, birth *none.LiveOwnerV1) (returned error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if owner == nil || ctx == nil || receipt == nil || receipt.boot == nil || receipt.boot.owner != owner || birth == nil {
		return sessionLeaseError("the unwalled warm proof has no same original native owner")
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.native == nil || !owner.Valid(owner.native.id) || owner.startup != receipt.boot || !receipt.boot.ready || receipt.noneBirth != birth || receipt.noneConnection == nil || receipt.need != nil || len(receipt.rawNeed) != 0 || receipt.boot.runtime != nil || receipt.spec.VM != nil || receipt.spec.RuntimeManifest != nil {
		return sessionLeaseError("the original unwalled warm issuer changed, is not ready, or belongs to a VM")
	}
	if err := birth.CheckSessionV1(ctx, owner.native.id, receipt.noneConnection); err != nil {
		return err
	}
	if err := receipt.noneHolder.checkLiveLockedV1(ctx, receipt, birth); err != nil {
		return err
	}
	native, ok := connection.(*net.UnixConn)
	if !ok {
		return sessionLeaseError("the unwalled proof has no original Unix connection")
	}
	identity, err := currentNativeHelperIdentityV1()
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, identity.Close()) }()
	// This SAME inherited description is the actual none creator's holder
	// role. Its distinct birth binding above is required; the auxiliary and
	// whole-unit holder duplicators retain their stricter owning-only rule.
	lease := owner.native
	lease.mu.RLock()
	if lease.name != holderLockFile {
		lease.mu.RUnlock()
		return sessionLeaseError("the original none creator has another native file role")
	}
	if err := lease.checkLocked(ctx); err != nil {
		lease.mu.RUnlock()
		return err
	}
	descriptor, err := unix.FcntlInt(lease.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		lease.mu.RUnlock()
		return err
	}
	holder := os.NewFile(uintptr(descriptor), "original-none-holder-proof")
	checkErr := lease.fileRef.CheckFile(lease.root, lease.name, holder, 0600)
	lease.mu.RUnlock()
	if checkErr != nil {
		return errors.Join(checkErr, holder.Close())
	}
	defer func() { returned = errors.Join(returned, holder.Close()) }()
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if err := proto.SendUnixFrameV1(native, proto.TResponse, raw, []*os.File{identity.executable, identity.user, identity.cgroup}); err != nil {
		return err
	}
	proof, err := json.Marshal(struct {
		Version int    `json:"v"`
		Primary string `json:"primary"`
	}{1, owner.native.id})
	if err != nil {
		return err
	}
	raw, err = json.Marshal(proto.Response{ID: response.ID, OK: true, Result: proof})
	if err != nil {
		return err
	}
	return proto.SendUnixFrameV1(native, proto.TResponse, raw, []*os.File{holder})
}
