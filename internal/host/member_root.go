package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This receipt is published only by the original MemberStart reservation,
// before the supervisor may launch it. It binds both native directory
// generations. A manifest's SandboxOf field cannot make this receipt.
type memberRootReceiptV1 struct {
	Primary    string    `json:"primary"`
	PrimaryRef purgeRef  `json:"primary_root"`
	Directory  purgeRef  `json:"directory"`
	CreatedAt  time.Time `json:"created_at"`
	extra      map[string]json.RawMessage
}

func (receipt *memberRootReceiptV1) UnmarshalJSON(data []byte) error {
	type plain memberRootReceiptV1
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"primary", "primary_root", "directory", "created_at"})
	*receipt = memberRootReceiptV1(value)
	receipt.extra = extra
	return err
}

func (receipt memberRootReceiptV1) MarshalJSON() ([]byte, error) {
	type plain memberRootReceiptV1
	return encodeFootprint(plain(receipt), receipt.extra)
}

func validateMemberRootReceiptsV1(receipts map[string]memberRootReceiptV1) error {
	for id, receipt := range receipts {
		if !validID(id) || !validID(receipt.Primary) || id == receipt.Primary || len(receipt.extra) != 0 || !receipt.PrimaryRef.validDirectory() || !receipt.Directory.validDirectory() || receipt.CreatedAt.IsZero() {
			return placementError("the original member directory receipt is unknown")
		}
	}
	return nil
}

func (start *MemberStart) recordCreatedMemberRootV1(ctx context.Context, parent *Manifest) (returned error) {
	if start == nil || start.reservation == nil || start.reservation.launched || start.Manifest == nil || parent == nil || parent.Wall == nil || parent.SandboxOf != "" || start.Manifest.SandboxOf != parent.ID || start.Manifest.ID != start.reservation.id {
		return memberError("the member has no original unlaunched directory reservation")
	}
	reservation := start.reservation
	if reservation.created == nil || reservation.dir == nil || reservation.parent == nil {
		return memberError("the member directory reservation ended")
	}
	publisher, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, publisher.close()) }()
	primary, err := cachefs.OpenPrivateRoot(paths.SessionDir(parent.ID), false)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, primary.Close()) }()
	directory, err := primary.Open(".")
	if err != nil {
		return err
	}
	primaryInfo, err := directory.Stat()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	receipt := memberRootReceiptV1{Primary: parent.ID, PrimaryRef: refOf(primaryInfo), Directory: refOf(reservation.created), CreatedAt: start.Manifest.CreatedAt}
	if err := validateMemberRootReceiptsV1(map[string]memberRootReceiptV1{reservation.id: receipt}); err != nil {
		return err
	}
	return withFootprintControlV1(publisher, func(control *placementControl, _ *os.Root) error {
		current, err := reservation.parent.Lstat(reservation.id)
		if err != nil || !os.SameFile(reservation.created, current) {
			return memberError("the original member directory changed before publication")
		}
		if err := errors.Join(receipt.Directory.matchesRoot(reservation.dir), receipt.PrimaryRef.matchesRoot(primary), checkNamedPlacementRoot(primary)); err != nil {
			return err
		}
		member, err := readMemberRootManifestV1(reservation.dir, reservation.id, receipt)
		if err != nil || member == nil {
			return errors.Join(memberError("the original member manifest was not published in its reserved root"), err)
		}
		if prior, exists := control.MemberRoots[reservation.id]; exists && (!samePurgeDirectoryRef(prior.Directory, receipt.Directory) || !samePurgeDirectoryRef(prior.PrimaryRef, receipt.PrimaryRef) || prior.Primary != receipt.Primary || !prior.CreatedAt.Equal(receipt.CreatedAt)) {
			return memberError("another original member directory already owns this identity")
		}
		if control.MemberRoots == nil {
			control.MemberRoots = map[string]memberRootReceiptV1{}
		}
		control.MemberRoots[reservation.id] = receipt
		return nil
	})
}

// A missing manifest is permitted only by the stopped consumer's separate
// native creation receipt and retained whole-primary stop. This reader alone
// never creates that permission.
func readMemberRootManifestV1(root *os.Root, id string, receipt memberRootReceiptV1) (*Manifest, error) {
	file, err := cachefs.OpenFile(root, "manifest.json", os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reference, err := cachefs.CaptureFile(root, "manifest.json", file, 0o600)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(proto.MaxFrame)+1))
	if err != nil || len(raw) > proto.MaxFrame {
		return nil, errors.Join(memberError("the original member manifest exceeds its inspection bound"), err)
	}
	var member Manifest
	if err := json.Unmarshal(raw, &member); err != nil {
		return nil, err
	}
	if len(member.extra) != 0 || member.path != "" || member.ID != id || member.SandboxOf != receipt.Primary || member.Wall == nil || member.Git != nil || !member.CreatedAt.Equal(receipt.CreatedAt) {
		return nil, memberError("the member manifest changed its original group or resource role")
	}
	if err := reference.CheckFile(root, "manifest.json", file, 0o600); err != nil {
		return nil, err
	}
	return &member, nil
}
