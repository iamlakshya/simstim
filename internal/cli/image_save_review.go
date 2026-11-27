package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/image"
)

// imageSaveOperation owns one prepared image on the original connection.
// Closing it drops an image that the client has not kept.
type imageSaveOperation interface {
	Review() image.SaveReview
	AllowFlagged()
	Keep(context.Context) (image.SaveResult, error)
	Close() error
}

var qImageFlagged = &question{cmd: "image save", flag: "--allow-flagged"}

func init() { questions = append(questions, qImageFlagged) }

func finishImageSave(cmd *cobra.Command, operation imageSaveOperation, p *prompter, allowFlagged, asJSON bool) (returned error) {
	defer func() { returned = errors.Join(returned, operation.Close()) }()
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	review := operation.Review()
	out := cmd.ErrOrStderr()
	if asJSON {
		out = jsonWarnings{w: out, code: "image_review"}
	}
	if err := showImageSaveReview(out, review); err != nil {
		return err
	}
	if len(review.Flagged) != 0 && !allowFlagged {
		if !p.on {
			return &client.UserError{
				Code: "image_flagged", Kind: client.KindRefused,
				Msg:  "the image changes include files that run code at login or at start",
				Hint: "Read the listed files in the sandbox, then run " + p.withFlags("--allow-flagged") + ".",
			}
		}
		yes, err := p.confirm(qImageFlagged, "Save the image with these reviewed changes?", "a yes after reviewing the listed files")
		if err != nil {
			return err
		}
		if !yes {
			return p.cancel()
		}
		allowFlagged = true
	}
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	if allowFlagged {
		operation.AllowFlagged()
	}
	var result image.SaveResult
	if err := grantQuestions(cmd, func(ctx context.Context) error {
		var err error
		result, err = operation.Keep(ctx)
		return err
	}); err != nil {
		return err
	}
	if asJSON {
		data, err := versioned(result)
		if err != nil {
			return err
		}
		if _, err := cmd.OutOrStdout().Write(data); err != nil {
			return imageSaveOutputError()
		}
		return nil
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", client.CleanLine(result.Image.Ref), client.CleanLine(result.Image.PlatformDigest), client.CleanLine(result.Image.RootHash))
	if err != nil {
		return imageSaveOutputError()
	}
	return nil
}

func showImageSaveReview(out io.Writer, review image.SaveReview) error {
	if _, err := fmt.Fprintf(out, "Image review: %d bytes.\n", review.Size); err != nil {
		return imageSaveOutputError()
	}
	for _, finding := range review.Flagged {
		if _, err := fmt.Fprintf(out, "Flagged %s: %s\n", client.CleanLine(finding.Path), client.CleanLine(finding.Reason)); err != nil {
			return imageSaveOutputError()
		}
	}
	for _, finding := range review.Excluded {
		if _, err := fmt.Fprintf(out, "Left out %s: %s\n", client.CleanLine(finding.Path), client.CleanLine(finding.Reason)); err != nil {
			return imageSaveOutputError()
		}
	}
	return nil
}

func imageSaveOutputError() error {
	return &client.UserError{Code: "output", Kind: client.KindFailed,
		Msg: "could not write the image save output", Hint: "Check the output destination, then run image save again."}
}
