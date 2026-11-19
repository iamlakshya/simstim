package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func imageSaveReviewFixture(t *testing.T, findings int) image.SaveReview {
	t.Helper()
	review := image.SaveReview{ID: strings.Repeat("c", 32), Ref: "devtools", Size: 4096}
	for i := range findings {
		review.Flagged = append(review.Flagged, image.SaveFinding{Path: fmt.Sprintf("/etc/profile.d/original-tool-%05d.sh", i), Reason: "shell start file"})
	}
	return review
}

func TestImageSavePagesResumeSameOwnerAfterQuestionWithoutRefetchOrResend(t *testing.T) {
	op, ctx, row, marker := imageSaveValuesFixture(t, TierOwn)
	if err := SetTierContext(t.Context(), op.conn.Host.Name, TierShared, op.conn.HostID(), ""); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(settingsPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("[secret_tiers]\nAPI_KEY = 'shared'\n")
	if err := file.Close(); err != nil || writeErr != nil {
		t.Fatal(err, writeErr)
	}
	review := imageSaveReviewFixture(t, 1200)
	saves, pages := 0, 0
	next := 0
	sharedGrantWire(t, op.conn, func(request proto.Request) any {
		if request.Op == "ls" {
			return []Session{*row}
		}
		if request.Op == "image.save" {
			saves++
			page, err := review.PageV1(0)
			if err != nil || page.Done {
				t.Fatal("fixture did not require later pages", err)
			}
			next = page.Next
			return page
		}
		if request.Op != "image.review" || bytes.Contains(request.Args, []byte("generated-image-scan-value")) {
			t.Fatal("later review used another operation or resent scan values", request.Op)
		}
		var args image.SaveReviewArgsV1
		if json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Session != row.ID || args.ID != review.ID || args.Offset != next {
			t.Fatal("later page changed its original connection/session/token/offset")
		}
		page, err := review.PageV1(args.Offset)
		if err != nil {
			t.Fatal(err)
		}
		next, pages = page.Next, pages+1
		if pages == 1 {
			// Revoke the live answer after a genuine intermediate response.
			// The next ordinary gate must ask on this SAME request again.
			op.request.mu.Lock()
			op.request.confirmed = nil
			op.request.mu.Unlock()
		}
		return page
	})
	if err := op.prepare(ctx, false); err == nil {
		t.Fatal("shared scanner values skipped their first terminal question")
	} else {
		ctx, err = ConfirmGrant(ctx, err)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := op.prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
	_, err = op.save(ctx)
	var question *ConfirmError
	if !errors.As(err, &question) || saves != 1 || pages != 1 || op.cleanupSaveID() != review.ID {
		t.Fatal("intermediate question lost its original pending save", saves, pages, err)
	}
	ctx, err = ConfirmGrant(ctx, err)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := op.save(ctx)
	if err != nil || saves != 1 || complete.ID != review.ID || len(complete.Flagged) != len(review.Flagged) || next != len(review.Flagged) {
		t.Fatal("same owner did not finish its original paged review", saves, pages, err)
	}
	if value, err := os.ReadFile(marker); err != nil || string(value) != "x" {
		t.Fatal("review continuation fetched local values again", err)
	}
}

func TestImageSaveWrongLaterPageRetainsOnlyOriginalCleanupToken(t *testing.T) {
	op, ctx, row, _ := imageSaveValuesFixture(t, TierOwn)
	review := imageSaveReviewFixture(t, 1200)
	saves, pages := 0, 0
	sharedGrantWire(t, op.conn, func(request proto.Request) any {
		if request.Op == "ls" {
			return []Session{*row}
		}
		if request.Op == "image.save" {
			saves++
			page, err := review.PageV1(0)
			if err != nil {
				t.Fatal(err)
			}
			return page
		}
		pages++
		var args image.SaveReviewArgsV1
		if request.Op != "image.review" || json.Unmarshal(request.Args, &args) != nil {
			t.Fatal("unexpected pending save continuation", request.Op)
		}
		page, err := review.PageV1(args.Offset)
		if err != nil {
			t.Fatal(err)
		}
		page.ID = strings.Repeat("d", 32)
		return page
	})
	if err := op.prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := op.save(ctx); err == nil {
			t.Fatal("foreign page became a complete original review")
		}
	}
	if op.cleanupSaveID() != review.ID || op.review != nil || saves != 1 || pages != 1 {
		t.Fatal("bad page replaced the original token or caused a retry", saves, pages)
	}
}
