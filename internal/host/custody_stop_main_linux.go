//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"github.com/iamlakshya/simstim/internal/proto"
)

// WriteManagerStopRoleV1 lends only this actual original main's held role.
// The supervisor owns the true accepted native scope and serialises its line.
// No descriptor, saved row or externally supplied role object is accepted.
func (main *ManagerCustodyMainV1) WriteManagerStopRoleV1(ctx context.Context, owner *OwnedSupervisorLease, connection net.Conn, response proto.Response, index uint64) error {
	if main == nil || main.owner != owner || index >= custodyBundleSizeV1 {
		return sessionLeaseError("the custody stop role has no exact original main")
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	if err := main.checkLiveV1(ctx); err != nil {
		return err
	}
	if main.retainedStop || main.deposited {
		return sessionLeaseError("the original main already selected another stop lifetime")
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	role := main.rolesV1()[index]
	return sendStartupAncillary(connection, proto.TResponse, raw, role.file)
}

// AcceptManagerStopV1 checks the same actual original main before a trusted
// stop intent is recorded. It neither deposits nor claims completed closure.
func (main *ManagerCustodyMainV1) AcceptManagerStopV1(ctx context.Context, owner *OwnedSupervisorLease) error {
	if main == nil || main.owner != owner {
		return sessionLeaseError("the manager stop has another original main")
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	if err := main.checkLiveV1(ctx); err != nil {
		return err
	}
	if main.retainedStop || main.deposited {
		return sessionLeaseError("the original custody main already selected another stop lifetime")
	}
	entries, err := main.storeEntriesV1(ctx)
	if err != nil || len(entries) != 0 {
		return errors.Join(sessionLeaseError("the original custody manager store is not empty before stop"), err)
	}
	return main.manager.checkCustodyPinV1(ctx, UnitName(owner.native.id))
}
