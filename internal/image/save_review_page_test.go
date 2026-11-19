package image

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSavedReviewPagesPreserveTheFullHundredThousandFindingVector(t *testing.T) {
	review := SaveReview{ID: strings.Repeat("a", 32), Ref: "tools", Size: convertedBound(4096), Excluded: []SaveFinding{{Path: "/work", Reason: "runtime filesystem"}}}
	for index := 0; index < SaveReviewFindingsMax; index++ {
		review.Flagged = append(review.Flagged, SaveFinding{Path: fmt.Sprintf("/usr/local/bin/tool-%06d-%s", index, strings.Repeat("x", 32)), Reason: "file in the original image PATH"})
	}
	encoded, err := json.Marshal(review)
	if err != nil || len(encoded) <= proto.MaxFrame {
		t.Fatal("the dense review fixture does not cross the actual response boundary", err)
	}
	if err := review.Validate("tools"); err != nil {
		t.Fatal("aggregate validation lowered the declared review bound", err)
	}
	var collector SaveReviewCollectorV1
	offset, pages := 0, 0
	for {
		page, err := review.PageV1(offset)
		if err != nil {
			t.Fatal(err)
		}
		if err := collector.Append(page, "tools"); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(page)
		if err != nil || len(payload) > SaveReviewPageBytes {
			t.Fatal("a complete review page exceeded its carrier", err)
		}
		if err := proto.NewWriter(io.Discard).WriteJSON(proto.TResponse, proto.Response{ID: math.MaxUint64, OK: true, Result: payload}); err != nil {
			t.Fatal("the complete page response did not fit its real protocol", err)
		}
		pages++
		offset = page.Next
		if page.Done {
			break
		}
		if _, err := collector.Review(); err == nil {
			t.Fatal("an incomplete review was published")
		}
	}
	actual, err := collector.Review()
	if err != nil || pages < 2 || !reflect.DeepEqual(actual, review) {
		t.Fatal("paged review lost original order, totals or findings", err)
	}
	actual.Flagged[0].Path = "/caller-mutation"
	again, err := collector.Review()
	if err != nil || again.Flagged[0].Path == "/caller-mutation" {
		t.Fatal("review returned borrowed aggregate storage", err)
	}
}

func TestSavedReviewPagingRefusesChangedTokenOffsetTotalsAndOrder(t *testing.T) {
	review := SaveReview{ID: strings.Repeat("a", 32), Ref: "tools", Size: convertedBound(4096), Flagged: []SaveFinding{{Path: "/etc/profile", Reason: "shell start file"}}, Excluded: []SaveFinding{{Path: "/work", Reason: "runtime filesystem"}}}
	first, err := review.PageV1(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SaveReviewPageV1){
		func(page *SaveReviewPageV1) { page.ID = strings.Repeat("b", 32) },
		func(page *SaveReviewPageV1) { page.Ref = "elsewhere" },
		func(page *SaveReviewPageV1) { page.Offset++ },
		func(page *SaveReviewPageV1) { page.Next++ },
		func(page *SaveReviewPageV1) { page.FlaggedTotal++ },
		func(page *SaveReviewPageV1) {
			page.Flagged = nil
			page.Excluded = append(page.Excluded, SaveFinding{Path: "/other", Reason: "changed kind"})
		},
	} {
		changed := first
		changed.Flagged = slices.Clone(first.Flagged)
		changed.Excluded = slices.Clone(first.Excluded)
		change(&changed)
		if changed.Validate("tools", review.ID, 0) == nil {
			t.Fatal("a changed review correlation or vector entered the same save")
		}
	}
	if _, err := review.PageV1(-1); err == nil {
		t.Fatal("a negative cursor entered the original review")
	}
	var collector SaveReviewCollectorV1
	if err := collector.Append(first, "tools"); err != nil {
		t.Fatal(err)
	}
	if err := collector.Append(first, "tools"); err == nil {
		t.Fatal("a replayed final page extended a completed review")
	}
}
