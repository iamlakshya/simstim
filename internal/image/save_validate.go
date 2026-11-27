package image

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func saveProtocol(message string) error {
	return &proto.Error{Code: "image_protocol", Message: message, Hint: "Upgrade the helper and repeat image save on the original host."}
}

// Validate checks an untrusted review against the original requested ref.
// A reviewed save ID can only be sent back on that same accepted connection.
func (review SaveReview) Validate(ref string) error {
	if err := review.validateHeader(ref); err != nil {
		return err
	}
	for _, group := range [][]SaveFinding{review.Flagged, review.Excluded} {
		if err := validateSaveFindings(group); err != nil {
			return err
		}
	}
	return nil
}

func (review SaveReview) validateHeader(ref string) error {
	if !ValidSaveRef(ref) || review.Ref != ref || len(review.ID) != 32 || !supply.IsHex64(review.ID+review.ID) || review.Size <= 0 || review.Size > maxOutput || len(review.Flagged) > SaveReviewFindingsMax || len(review.Excluded) > SaveReviewFindingsMax {
		return saveProtocol("the saved-image review header is invalid")
	}
	return nil
}

func validateSaveFindings(findings []SaveFinding) error {
	if len(findings) > SaveReviewFindingsMax {
		return saveProtocol("the saved-image review exceeds its finding count bound")
	}
	paths := 0
	for _, finding := range findings {
		paths += len(finding.Path)
		if !savedAbsolutePath(finding.Path) || finding.Reason == "" || api.CleanText(finding.Reason, SaveReviewReasonMax) != finding.Reason || paths > SaveReviewPathBytes {
			return saveProtocol("the saved-image review has an invalid or oversized finding")
		}
	}
	return nil
}

// Validate binds saved-image metadata to the original host and local alias.
// It deliberately does not invoke OCI reference or manifest validation.
func (result SaveResult) Validate(ref, hostID string) error {
	record := result.Image
	if !ValidSaveRef(ref) || len(hostID) != 32 || strings.Trim(hostID, "0123456789abcdef") != "" || result.HostID != hostID || record.Ref != ref || !ValidDigest(record.Digest) || record.Digest != record.PlatformDigest || record.Arch != "amd64" && record.Arch != "arm64" || !supply.IsHex64(record.RootHash) || record.DataBlocks <= 0 || record.DataBlocks > MaxUncompressed/4096 || record.Size != convertedBound(record.DataBlocks*4096) || record.ConvertedAt.IsZero() || record.UsedAt.IsZero() {
		return saveProtocol("the host returned saved-image metadata outside the original request")
	}
	if record.SavedHost == nil || record.SavedHost.Validate() != nil || record.SavedHost.Ref != ref || record.SavedHost.HostID != hostID || record.SavedHost.Digest != record.Digest {
		return saveProtocol("the saved-image result has no matching typed original host identity")
	}
	return nil
}
