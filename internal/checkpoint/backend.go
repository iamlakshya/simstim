package checkpoint

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// SnapshotV1 owns a read-only selected disk and metadata. It can outlive the
// original running disk borrow and grants no current-disk or stop authority.
// Each backend accepts only snapshots issued by its actual private producer.
type SnapshotV1 interface {
	Entry() Entry
	DiskBytesV1() int64
	CheckV1(context.Context) error
	Close() error
}

// BackendV1 keeps storage operations with their actual native producer. A
// separate-account backend delegates fixed roles to its original root helper;
// it must never weaken private cache ownership or adopt roots by a saved path.
type BackendV1 interface {
	Probe(context.Context) error
	CheckSpaceV1(context.Context, int64) error
	List(context.Context) ([]Entry, error)
	Create(context.Context, time.Time, Metadata, int) (Entry, error)
	Trim(context.Context, int) error
	Select(context.Context, string) (SnapshotV1, error)
	PrepareRestore(context.Context, SnapshotV1, TransactionV1, func(context.Context) error) (PreparedRestoreV1, error)
}

// PreparedRestoreV1 retains the actual original and replacement disks until
// the same native owner settles commit or rollback. Close refuses an unsettled
// transaction. Journal is data only and cannot recreate this interface.
type PreparedRestoreV1 interface {
	Journal() TransactionV1
	Commit(context.Context) error
	Rollback(context.Context) error
	Finalize(context.Context) error
	Close() error
}

// The phases describe physical disk progress, independently of host manifest
// CAS or journal durability. The host owns those separate commit decisions.
const (
	RestorePreparedV1   = "prepared"
	RestoreExchangedV1  = "exchanged"
	RestoreRolledBackV1 = "rolled_back"
	RestoreFinalizedV1  = "finalized"
)

// DiskRoleV1 is a journal description of a retained disk, never authority to
// open, replace or delete a named file. Its reference is evidence only.
type DiskRoleV1 struct {
	Role      string            `json:"role"`
	Name      string            `json:"name"`
	Reference cachefs.Reference `json:"reference"`
}

// TransactionV1 is bounded journal data. Generation is a fresh 32-character
// lowercase hexadecimal transaction identifier, not a source capability.
// Prepare uses only Generation and Safety as input; the actual backend fills
// Selected, Phase and both disk roles from its retained native objects.
type TransactionV1 struct {
	Version     int        `json:"v"`
	Generation  string     `json:"generation"`
	Phase       string     `json:"phase"`
	Selected    Entry      `json:"selected"`
	Safety      Entry      `json:"safety"`
	Original    DiskRoleV1 `json:"original"`
	Replacement DiskRoleV1 `json:"replacement"`
}

// ValidatePrepareV1 checks request data only. It grants no backend, snapshot,
// native stopped owner or filesystem capability.
func ValidatePrepareV1(transaction TransactionV1) error {
	if transaction.Version != 1 || len(transaction.Generation) != 32 || transaction.Safety.Time.IsZero() {
		return failure("the rewind transaction version, generation or safety time is invalid", nil)
	}
	for _, value := range transaction.Generation {
		if value < '0' || value > '9' && value < 'a' || value > 'f' {
			return failure("the rewind transaction generation is not lowercase hexadecimal", nil)
		}
	}
	if _, err := metadataBytes(transaction.Safety.Metadata); err != nil {
		return failure("the rewind transaction has invalid safety metadata", err)
	}
	return nil
}

func copyTransactionV1(transaction TransactionV1) TransactionV1 {
	transaction.Selected.Metadata = cloneMetadata(transaction.Selected.Metadata)
	transaction.Safety.Metadata = cloneMetadata(transaction.Safety.Metadata)
	return transaction
}
