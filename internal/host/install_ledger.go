package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

type installFileV1 struct {
	Item     api.InstallItemV1 `json:"item"`
	Ref      purgeRef          `json:"ref"`
	Prepared string            `json:"prepared,omitempty"`
}
type installVersionV1 struct {
	Directory purgeRef                 `json:"directory"`
	Created   bool                     `json:"created"`
	Files     map[string]installFileV1 `json:"files"`
}
type installLinkV1 struct {
	Ref    purgeRef `json:"ref"`
	Target string   `json:"target"`
}
type installCurrentIntentV1 struct {
	Before   *installLinkV1 `json:"before,omitempty"`
	After    installLinkV1  `json:"after"`
	Prepared string         `json:"prepared"`
	Capture  string         `json:"capture"`
}
type installLedgerV1 struct {
	Version        int                         `json:"v"`
	HostID         string                      `json:"host_id"`
	Scope          string                      `json:"scope"`
	UID            int                         `json:"uid"`
	Versions       map[string]installVersionV1 `json:"versions"`
	Current        *installLinkV1              `json:"current,omitempty"`
	PendingCurrent *installCurrentIntentV1     `json:"pending_current,omitempty"`
}

func validateInstallLedgerV1(raw []byte, control *placementControl) (installLedgerV1, error) {
	var ledger installLedgerV1
	if !api.ExactJSONFields(raw, ledger) || json.Unmarshal(raw, &ledger) != nil || ledger.Version != 1 || ledger.HostID != control.HostID || ledger.Scope != control.Scope || ledger.UID != control.UID || ledger.Versions == nil {
		return ledger, placementError("the helper installation archive has an unknown owner or schema")
	}
	for version, record := range ledger.Versions {
		if !validInstallVersionV1(version) || !record.Directory.validDirectory() || record.Files == nil {
			return ledger, placementError("the helper version has no original native directory receipt")
		}
		for name, file := range record.Files {
			if name != file.Item.Name || !validInstallItemV1(file.Item) || len(file.Ref.extra) != 0 || file.Ref.Inode == 0 || file.Ref.UID != uint64(os.Getuid()) || file.Ref.Mode != 0o755 || file.Ref.Size != file.Item.Size || file.Ref.ModTime.IsZero() || file.Prepared != "" && (!strings.HasPrefix(file.Prepared, ".install-part-") || !ValidHostID(strings.TrimPrefix(file.Prepared, ".install-part-"))) {
				return ledger, placementError("the installed file has an unknown role or native producer receipt")
			}
		}
	}
	if ledger.Current != nil && !validInstallLinkV1(*ledger.Current) {
		return ledger, placementError("the current helper link has an unknown native receipt")
	}
	if pending := ledger.PendingCurrent; pending != nil {
		if !validInstallLinkV1(pending.After) || pending.Before != nil && !validInstallLinkV1(*pending.Before) || !strings.HasPrefix(pending.Prepared, ".current-") || !ValidHostID(strings.TrimPrefix(pending.Prepared, ".current-")) || !strings.HasPrefix(pending.Capture, ".current-old-") || !ValidHostID(strings.TrimPrefix(pending.Capture, ".current-old-")) {
			return ledger, placementError("the prepared current link has an unknown native intent")
		}
	}
	return ledger, nil
}

func readInstallLedgerV1(ctx context.Context, guard *os.Root, control *placementControl) (installLedgerV1, error) {
	if _, pending := control.ArchiveWrites["installs.json"]; pending {
		if err := replayInstallArchiveV1(ctx, guard, control); err != nil {
			return installLedgerV1{}, err
		}
	}
	before, err := metadataBefore(guard, "installs.json")
	if err != nil {
		return installLedgerV1{}, err
	}
	if before == nil {
		if _, recorded := control.Archives["installs.json"]; recorded {
			return installLedgerV1{}, placementError("the recorded installation archive is missing")
		}
		return installLedgerV1{Version: 1, HostID: control.HostID, Scope: control.Scope, UID: control.UID, Versions: map[string]installVersionV1{}}, nil
	}
	if !control.Archives["installs.json"].matches(before) {
		return installLedgerV1{}, placementError("the installation archive changed its original native generation")
	}
	file, err := cachefs.OpenFile(guard, "installs.json", os.O_RDONLY, 0o600)
	if err != nil {
		return installLedgerV1{}, err
	}
	defer file.Close()
	var ledger installLedgerV1
	err = validateArchiveDescriptor(ctx, file, func(raw []byte) error { var err error; ledger, err = validateInstallLedgerV1(raw, control); return err })
	if err != nil {
		return ledger, err
	}
	info, err := cachefs.ValidateFile(guard, "installs.json", file, 0o600)
	if err != nil || !control.Archives["installs.json"].matches(info) {
		return ledger, placementError("the installation archive changed during inspection")
	}
	return ledger, nil
}

func writeInstallLedgerV1(ctx context.Context, guard *os.Root, control *placementControl, ledger installLedgerV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replayInstallArchiveV1(ctx, guard, control); err != nil {
		return err
	}
	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	if _, err := validateInstallLedgerV1(data, control); err != nil {
		return err
	}
	before, err := metadataBefore(guard, "installs.json")
	if err != nil {
		return err
	}
	if before != nil && !control.Archives["installs.json"].matches(before) {
		return placementError("the helper archive has another native generation")
	}
	after, err := writeOwnedMetadataIntent(guard, "installs.json", data, before, func(info os.FileInfo, temporary string) error {
		if control.ArchiveWrites == nil {
			control.ArchiveWrites = map[string]metadataWriteIntent{}
		}
		intent := metadataWriteIntent{After: refOf(info), Temporary: temporary}
		if before != nil {
			ref := refOf(before)
			intent.Before = &ref
		}
		control.ArchiveWrites["installs.json"] = intent
		return writePlacementControl(guard, *control)
	})
	if err != nil {
		return err
	}
	if control.Archives == nil {
		control.Archives = map[string]purgeRef{}
	}
	control.Archives["installs.json"] = refOf(after)
	delete(control.ArchiveWrites, "installs.json")
	return writePlacementControl(guard, *control)
}

func joinInstallCloseV1(returned *error, root *os.Root) {
	*returned = errors.Join(*returned, root.Close())
}
