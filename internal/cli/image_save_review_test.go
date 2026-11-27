package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/image"
)

type imageSaveScene struct {
	review                image.SaveReview
	allow, keep, close    int
	keepError, closeError error
}

func (s *imageSaveScene) Review() image.SaveReview { return s.review }
func (s *imageSaveScene) AllowFlagged()            { s.allow++ }
func (s *imageSaveScene) Close() error             { s.close++; return s.closeError }
func (s *imageSaveScene) Keep(ctx context.Context) (image.SaveResult, error) {
	s.keep++
	if err := ctx.Err(); err != nil {
		return image.SaveResult{}, err
	}
	return image.SaveResult{HostID: "host-id", Image: image.Record{Ref: "tools", PlatformDigest: "sha256:record", RootHash: "root"}}, s.keepError
}

func imageSaveTestCommand(ctx context.Context, out, review io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "save"}
	cmd.SetContext(ctx)
	cmd.SetOut(out)
	cmd.SetErr(review)
	return cmd
}

func TestImageSaveFlaggedWithoutInputDropsOriginalPreparation(t *testing.T) {
	var out, review bytes.Buffer
	scene := &imageSaveScene{review: image.SaveReview{Size: 4096, Flagged: []image.SaveFinding{{Path: "/etc/profile", Reason: "shell start file"}}, Excluded: []image.SaveFinding{{Path: "/root/.ssh", Reason: "credentials"}}}}
	p := &prompter{on: false, line: []string{"image", "save", "box/work", "tools"}, flagsAt: 4}
	err := finishImageSave(imageSaveTestCommand(context.Background(), &out, &review), scene, p, false, false)
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "image_flagged" || user.Kind != client.KindRefused || !strings.Contains(user.Hint, "--allow-flagged") {
		t.Fatalf("missing explicit review refusal: %v", err)
	}
	if scene.keep != 0 || scene.allow != 0 || scene.close != 1 || out.Len() != 0 {
		t.Fatalf("refused review changed preparation: keep=%d allow=%d close=%d output=%q", scene.keep, scene.allow, scene.close, out.String())
	}
	if !strings.Contains(review.String(), "Flagged /etc/profile: shell start file") || !strings.Contains(review.String(), "Left out /root/.ssh: credentials") {
		t.Fatalf("review omitted findings: %q", review.String())
	}
}

func TestImageSaveReviewConsentAndDeclineKeepTheirOriginalOperation(t *testing.T) {
	for _, test := range []struct {
		name, input string
		flag        bool
		wantKeep    int
	}{
		{name: "yes", input: "yes\n", wantKeep: 1},
		{name: "no", input: "no\n"},
		{name: "default", input: "\n"},
		{name: "explicit flag", flag: true, wantKeep: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, review bytes.Buffer
			scene := &imageSaveScene{review: image.SaveReview{Flagged: []image.SaveFinding{{Path: "/etc/profile", Reason: "shell start file"}}}}
			p := testPrompts(test.input, &review)
			err := finishImageSave(imageSaveTestCommand(context.Background(), &out, &review), scene, p, test.flag, false)
			if test.wantKeep == 0 && !isCancel(err) || test.wantKeep != 0 && err != nil {
				t.Fatalf("review result: %v", err)
			}
			if scene.keep != test.wantKeep || scene.allow != test.wantKeep || scene.close != 1 {
				t.Fatalf("keep=%d allow=%d close=%d", scene.keep, scene.allow, scene.close)
			}
			if test.flag && strings.Contains(review.String(), "[y/N]") {
				t.Fatal("explicit review flag still asked a question")
			}
		})
	}
}

func TestImageSaveJSONReviewKeepsOnlyVersionedDataOnOutput(t *testing.T) {
	var out, review bytes.Buffer
	scene := &imageSaveScene{review: image.SaveReview{Size: 4096, Excluded: []image.SaveFinding{{Path: "/root/.config", Reason: "credential files"}}}}
	err := finishImageSave(imageSaveTestCommand(context.Background(), &out, &review), scene, &prompter{}, false, true)
	if err != nil || scene.keep != 1 || scene.close != 1 {
		t.Fatalf("save: %v, keep=%d close=%d", err, scene.keep, scene.close)
	}
	var result struct {
		V      int    `json:"v"`
		HostID string `json:"host_id"`
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || result.V != 1 || result.HostID != "host-id" {
		t.Fatalf("not one saved result: %q", out.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(review.String()), "\n") {
		var warning struct {
			V       int `json:"v"`
			Warning struct {
				Code string `json:"code"`
			} `json:"warning"`
		}
		if json.Unmarshal([]byte(line), &warning) != nil || warning.V != 1 || warning.Warning.Code != "image_review" {
			t.Fatalf("plain text leaked into JSON stderr: %q", line)
		}
	}
}

type cancelImageReviewWriter struct{ cancel context.CancelFunc }

func (w cancelImageReviewWriter) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }

func TestImageSaveCancellationAndKeepFailureStillCloseOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	scene := &imageSaveScene{}
	err := finishImageSave(imageSaveTestCommand(ctx, &out, cancelImageReviewWriter{cancel}), scene, &prompter{}, false, false)
	if !errors.Is(err, context.Canceled) || scene.keep != 0 || scene.close != 1 || out.Len() != 0 {
		t.Fatalf("cancelled review: %v keep=%d close=%d output=%q", err, scene.keep, scene.close, out.String())
	}
	keepError, closeError := errors.New("keep failed"), errors.New("drop failed")
	scene = &imageSaveScene{keepError: keepError, closeError: closeError}
	err = finishImageSave(imageSaveTestCommand(context.Background(), &out, io.Discard), scene, &prompter{}, false, false)
	if !errors.Is(err, keepError) || !errors.Is(err, closeError) || scene.keep != 1 || scene.close != 1 || out.Len() != 0 {
		t.Fatalf("failed keep cleanup: %v keep=%d close=%d output=%q", err, scene.keep, scene.close, out.String())
	}
}
