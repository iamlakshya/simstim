package image

import (
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

const (
	SaveReviewPageBytes   = proto.ImageChangesPageBytes
	SaveReviewFindingsMax = proto.ImageChangesMax
	SaveReviewPathBytes   = proto.ImageChangesBytes
	SaveReviewReasonMax   = 256
)

// SaveReviewArgsV1 requests another window on the SAME accepted save object.
// The ID cannot select a source or be moved to a new session connection.
type SaveReviewArgsV1 struct {
	Version int    `json:"v"`
	Session string `json:"session"`
	ID      string `json:"id"`
	Offset  int    `json:"offset"`
}

// SaveReviewPageV1 preserves the immutable flagged-then-excluded ordering.
// Each complete encoded page is at most 32 KiB, including its own metadata.
type SaveReviewPageV1 struct {
	Version       int           `json:"v"`
	ID            string        `json:"id"`
	Ref           string        `json:"ref"`
	Size          int64         `json:"size"`
	Flagged       []SaveFinding `json:"flagged"`
	Excluded      []SaveFinding `json:"excluded"`
	Offset        int           `json:"offset"`
	Next          int           `json:"next"`
	Done          bool          `json:"done"`
	FlaggedTotal  int           `json:"flagged_total"`
	ExcludedTotal int           `json:"excluded_total"`
}

// PageV1 copies one bounded window. The native producer validates the whole
// review once before publishing it; paging does not rescan earlier windows.
func (review SaveReview) PageV1(offset int) (SaveReviewPageV1, error) {
	if err := review.validateHeader(review.Ref); err != nil {
		return SaveReviewPageV1{}, err
	}
	flagged, excluded, next, err := saveReviewWindow(review.Flagged, review.Excluded, offset)
	if err != nil {
		return SaveReviewPageV1{}, err
	}
	page := SaveReviewPageV1{Version: 1, ID: review.ID, Ref: review.Ref, Size: review.Size, Flagged: flagged, Excluded: excluded, Offset: offset, Next: next, Done: next == len(review.Flagged)+len(review.Excluded), FlaggedTotal: len(review.Flagged), ExcludedTotal: len(review.Excluded)}
	return page, page.Validate(review.Ref, review.ID, offset)
}

func saveReviewWindow(flagged, excluded []SaveFinding, offset int) ([]SaveFinding, []SaveFinding, int, error) {
	total := len(flagged) + len(excluded)
	if len(flagged) > SaveReviewFindingsMax || len(excluded) > SaveReviewFindingsMax || offset < 0 || offset > total {
		return nil, nil, 0, saveProtocol("the saved-image review offset is outside its original vector")
	}
	next, bytes := offset, 0
	for next < total {
		finding := SaveFinding{}
		if next < len(flagged) {
			finding = flagged[next]
		} else {
			finding = excluded[next-len(flagged)]
		}
		if err := validateSaveFindings([]SaveFinding{finding}); err != nil {
			return nil, nil, 0, err
		}
		encoded, err := json.Marshal(finding)
		if err != nil {
			return nil, nil, 0, err
		}
		if bytes+len(encoded)+1 > SaveReviewPageBytes-1024 {
			break
		}
		bytes += len(encoded) + 1
		next++
	}
	if next == offset && next != total {
		return nil, nil, 0, saveProtocol("a saved-image finding cannot fit its complete page")
	}
	var first, second []SaveFinding
	if offset < len(flagged) {
		first = slices.Clone(flagged[offset:min(next, len(flagged))])
	}
	if next > len(flagged) {
		second = slices.Clone(excluded[max(offset-len(flagged), 0) : next-len(flagged)])
	}
	return first, second, next, nil
}

// Validate binds a page to the original ID/ref/offset. id may be empty only
// when receiving the first page from the actual accepted save operation.
func (page SaveReviewPageV1) Validate(ref, id string, offset int) error {
	if page.Version != 1 || page.Ref != ref || !ValidSaveRef(ref) || len(page.ID) != 32 || !supply.IsHex64(page.ID+page.ID) || id != "" && page.ID != id || id == "" && offset != 0 || page.Size <= 0 || page.Size > maxOutput {
		return saveProtocol("the saved-image review page has invalid original identity")
	}
	if err := validateSavePagePosition(page.Offset, page.Next, page.Done, page.FlaggedTotal, page.ExcludedTotal, len(page.Flagged), len(page.Excluded), offset); err != nil {
		return err
	}
	if err := validateSaveFindings(page.Flagged); err != nil {
		return err
	}
	if err := validateSaveFindings(page.Excluded); err != nil {
		return err
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > SaveReviewPageBytes {
		return saveProtocol("the complete saved-image review page exceeds its bound")
	}
	return nil
}

func validateSavePagePosition(offset, next int, done bool, flaggedTotal, excludedTotal, flagged, excluded, expected int) error {
	if flaggedTotal < 0 || excludedTotal < 0 || flaggedTotal > SaveReviewFindingsMax || excludedTotal > SaveReviewFindingsMax || offset != expected || offset < 0 || offset > flaggedTotal+excludedTotal || next != offset+flagged+excluded || next > flaggedTotal+excludedTotal || done != (next == flaggedTotal+excludedTotal) || next == offset && !done {
		return saveProtocol("the saved-image review page changed its original position or totals")
	}
	wantedFlagged := min(flaggedTotal-offset, flagged+excluded)
	if wantedFlagged < 0 {
		wantedFlagged = 0
	}
	if flagged != wantedFlagged || excluded > 0 && offset+flagged < flaggedTotal {
		return saveProtocol("the saved-image review page changed its flagged/excluded order")
	}
	return nil
}

// SaveReviewCollectorV1 is bounded ephemeral transport data. It neither
// authorises a keep nor replaces the original accepted source/connection.
type SaveReviewCollectorV1 struct {
	review                            SaveReview
	flaggedTotal, excludedTotal, next int
	pathBytes                         [2]int
	started, complete                 bool
}

func (collector *SaveReviewCollectorV1) Append(page SaveReviewPageV1, ref string) error {
	if collector.complete {
		return saveProtocol("the original saved-image review already ended")
	}
	id := ""
	if collector.started {
		id = collector.review.ID
	}
	if err := page.Validate(ref, id, collector.next); err != nil {
		return err
	}
	if collector.started && (collector.review.Ref != page.Ref || collector.review.Size != page.Size || collector.flaggedTotal != page.FlaggedTotal || collector.excludedTotal != page.ExcludedTotal) {
		return saveProtocol("the saved-image review changed its immutable metadata")
	}
	bytes := collector.pathBytes
	for group, findings := range [][]SaveFinding{page.Flagged, page.Excluded} {
		for _, finding := range findings {
			bytes[group] += len(finding.Path)
			if bytes[group] > SaveReviewPathBytes {
				return saveProtocol("the original saved-image review exceeds its aggregate path bound")
			}
		}
	}
	if !collector.started {
		collector.review = SaveReview{ID: page.ID, Ref: page.Ref, Size: page.Size}
		collector.flaggedTotal, collector.excludedTotal = page.FlaggedTotal, page.ExcludedTotal
		collector.started = true
	}
	collector.review.Flagged = append(collector.review.Flagged, page.Flagged...)
	collector.review.Excluded = append(collector.review.Excluded, page.Excluded...)
	collector.pathBytes, collector.next, collector.complete = bytes, page.Next, page.Done
	return nil
}

// Review returns an owned complete vector only after the exact final page.
func (collector *SaveReviewCollectorV1) Review() (SaveReview, error) {
	if !collector.started || !collector.complete || len(collector.review.Flagged) != collector.flaggedTotal || len(collector.review.Excluded) != collector.excludedTotal {
		return SaveReview{}, saveProtocol("the original saved-image review is incomplete")
	}
	if err := collector.review.Validate(collector.review.Ref); err != nil {
		return SaveReview{}, err
	}
	result := collector.review
	result.Flagged, result.Excluded = slices.Clone(result.Flagged), slices.Clone(result.Excluded)
	return result, nil
}
