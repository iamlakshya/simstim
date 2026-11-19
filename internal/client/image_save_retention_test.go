package client

import (
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestImageSaveRetainsActualReviewAfterPostResponseSourceRefusal(t *testing.T) {
	op, ctx, row, _ := imageSaveValuesFixture(t, TierOwn)
	saves := 0
	sharedGrantWire(t, op.conn, func(request proto.Request) any {
		if request.Op == "ls" {
			return []Session{*row}
		}
		if request.Op != "image.save" {
			t.Fatal("unexpected image-save operation", request.Op)
		}
		saves++
		// The external local settings edit is a restriction at the final
		// gate. It cannot erase the actual complete ACK or renew its source.
		if err := os.WriteFile(settingsPath(), []byte("[secrets]\nAPI_KEY = \"printf changed-value-command\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		page, err := imageSaveReviewFixture(t, 0).PageV1(0)
		if err != nil {
			t.Fatal(err)
		}
		return page
	})
	if err := op.prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := op.save(ctx); err == nil {
		t.Fatal("changed original source was accepted after the response")
	}
	if op.review == nil || op.review.ID != strings.Repeat("c", 32) || !op.attempted {
		t.Fatal("post-response refusal erased the actual review/attempt fact")
	}
	if _, err := op.save(ctx); err == nil || saves != 1 {
		t.Fatal("cached response renewed the changed source or sent another save", saves, err)
	}
}
