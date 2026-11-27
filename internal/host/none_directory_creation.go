package host

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/fsbirth"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

const noneDirectoryCreationBoundV1 = 4096

// This is only canonical DATA produced by the actual None mkdir/open. It is
// never a runtime manifest, native capability, stopped source or path grant.
type noneDirectoryCreationV1 struct {
	Version        int               `json:"v"`
	Primary        string            `json:"primary"`
	Boot           string            `json:"boot"`
	Generation     string            `json:"generation"`
	Parent         cachefs.Reference `json:"parent"`
	Directory      cachefs.Reference `json:"directory"`
	ParentBirth    *fsbirth.DataV2   `json:"parent_birth"`
	DirectoryBirth *fsbirth.DataV2   `json:"directory_birth"`
}

func noneDirectoryOriginErrorV1(message string) error {
	return UserError("session_owner", message, "Preserve this session and its original runtime directory. Inspect its actual creation record and filesystem support before retrying; Force does not establish ownership.")
}

func decodeNoneDirectoryCreationV1(m *Manifest) (*noneDirectoryCreationV1, error) {
	if !NoneSupervisorShapeV1(m) || len(m.NoneDirectoryCreation) == 0 || len(m.NoneDirectoryCreation) > noneDirectoryCreationBoundV1 {
		return nil, noneDirectoryOriginErrorV1("the None directory has no supported original creator record")
	}
	var record noneDirectoryCreationV1
	fields, err := api.JSONObjectFields(m.NoneDirectoryCreation, "v", "primary", "boot", "generation", "parent", "directory", "parent_birth", "directory_birth")
	if err != nil || len(fields) != 8 || !api.ExactJSONFields(m.NoneDirectoryCreation, record) || json.Unmarshal(m.NoneDirectoryCreation, &record) != nil {
		return nil, noneDirectoryOriginErrorV1("the original None directory creation data is unknown or changed")
	}
	if record.Version != 1 || record.Primary != m.ID || !api.ValidSessionID(record.Primary) || record.Boot != m.Run.BootID || !auxiliaryBootIDV1(record.Boot) || record.Generation == "" || record.Generation != m.Run.Generation || len(record.Generation) > 64 || !record.Parent.Known() || !record.Parent.Directory || record.Parent.Mode != 0700 || !record.Directory.Known() || !record.Directory.Directory || record.Directory.Mode != 0700 {
		return nil, noneDirectoryOriginErrorV1("the original None directory differs from its exact protected run")
	}
	if record.ParentBirth != nil && !record.ParentBirth.Known() || record.DirectoryBirth != nil && !record.DirectoryBirth.Known() {
		return nil, noneDirectoryOriginErrorV1("the original None directory has an unsupported native generation record")
	}
	return &record, nil
}

// NoneDirectoryCreationDataV1 projects only this actual bound None creator's
// already captured mkdir/open DATA. A supplied Manifest cannot create it.
func (receipt *OriginalVMNeed) NoneDirectoryCreationDataV1(ctx context.Context, birth *none.LiveOwnerV1) (json.RawMessage, error) {
	if receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || receipt.boot == nil || receipt.boot.owner == nil || birth == nil {
		return nil, noneDirectoryOriginErrorV1("the None directory has no actual original birth and startup owner")
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	owner := receipt.boot.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if receipt.noneHolder == nil || receipt.noneBirth != birth || receipt.noneConnection == nil || owner.startup != receipt.boot || receipt.boot.ready || !owner.Valid(receipt.boot.loan.Primary) || owner.generation == "" || !receipt.boot.operation.Valid(receipt.boot.loan.Primary) {
		return nil, noneDirectoryOriginErrorV1("the original None creation publisher ended or changed")
	}
	origin := receipt.noneHolder
	if origin.self != origin || origin.receipt != receipt || origin.owner != owner || origin.creator != owner.pending || origin.birth != birth || origin.connection != receipt.noneConnection || origin.holder != owner.native || origin.file == nil || origin.file != owner.native.file || origin.generation != owner.generation || receipt.need != nil || len(receipt.rawNeed) != 0 || receipt.boot.runtime != nil || receipt.spec.VM != nil {
		return nil, noneDirectoryOriginErrorV1("the None directory publisher changed its actual original holder birth")
	}
	if err := observeNoneHolderV1(origin.file); err != nil {
		return nil, err
	}
	m, err := receipt.boot.operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !NoneSupervisorShapeV1(m) {
		return nil, noneDirectoryOriginErrorV1("the original directory publisher does not select the actual None role")
	}
	data, err := birth.DirectoryCreationDataV1(ctx)
	if err != nil {
		return nil, err
	}
	boot := BootID()
	if !validNoneMigrationBootV1(boot) {
		return nil, noneDirectoryOriginErrorV1("the None creator has no real current kernel boot epoch")
	}
	record := noneDirectoryCreationV1{Version: 1, Primary: receipt.boot.loan.Primary, Boot: boot, Generation: owner.generation, Parent: data.Parent, Directory: data.Directory, ParentBirth: data.ParentBirth, DirectoryBirth: data.DirectoryBirth}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(raw) > noneDirectoryCreationBoundV1 {
		return nil, noneDirectoryOriginErrorV1("the actual None directory creation data exceeds its bound")
	}
	return raw, nil
}
