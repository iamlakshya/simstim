package client

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
)

const (
	imageOriginOCI     = "oci"
	imageOriginRelease = "release-default"
	imageOriginSaved   = "saved-host"
)

// sandboxImageSelectionV1 is historical data issued by the original local
// creation owner. It is stored only inside that owner's existing record.
// Neither this description nor its codec grants a source, root or boot action.
type sandboxImageSelectionV1 struct {
	Version int           `json:"version"`
	Origin  string        `json:"origin"`
	Image   image.Request `json:"image"`
	Release string        `json:"release,omitempty"`
}

func cloneSandboxImageSelection(selection *sandboxImageSelectionV1) *sandboxImageSelectionV1 {
	if selection == nil {
		return nil
	}
	copy := *selection
	copy.Image = selection.Image.Clone()
	return &copy
}

func (selection sandboxImageSelectionV1) validate() error {
	if selection.Version != 1 || selection.Image.Transfer != "" || selection.Image.RootHash == "" || selection.Image.DataBlocks <= 0 {
		return imageSelectionHistoryError()
	}
	if err := selection.Image.Validate(); err != nil {
		return err
	}
	switch selection.Origin {
	case imageOriginOCI:
		if selection.Image.SavedHost != nil || selection.Release != "" {
			return imageSelectionHistoryError()
		}
	case imageOriginRelease:
		if selection.Image.SavedHost != nil || selection.Release == "" || len(selection.Release) > 128 || strings.ContainsAny(selection.Release, "/\\\x00\r\n\t ") {
			return imageSelectionHistoryError()
		}
		ref, err := image.ParseReference(selection.Image.Ref)
		if err != nil || ref.Identifier != selection.Image.Digest || selection.Image.PlatformDigest != selection.Image.Digest {
			return imageSelectionHistoryError()
		}
	case imageOriginSaved:
		if selection.Image.SavedHost == nil || selection.Release != "" {
			return imageSelectionHistoryError()
		}
	default:
		return imageSelectionHistoryError()
	}
	return nil
}

func decodeSandboxImageSelection(raw json.RawMessage) (*sandboxImageSelectionV1, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > 64<<10 || !api.ExactJSONFields(raw, sandboxImageSelectionV1{}) {
		return nil, imageSelectionHistoryError()
	}
	var selection sandboxImageSelectionV1
	if json.Unmarshal(raw, &selection) != nil {
		return nil, imageSelectionHistoryError()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !api.ExactJSONFields(fields["image"], image.Request{}) {
		return nil, imageSelectionHistoryError()
	}
	if err := selection.validate(); err != nil {
		return nil, err
	}
	return cloneSandboxImageSelection(&selection), nil
}

func encodeSandboxImageSelection(selection *sandboxImageSelectionV1) (json.RawMessage, error) {
	if selection == nil {
		return nil, nil
	}
	if err := selection.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(selection)
}

// verifyHistoricalImagePolicy checks the original creation image directly.
// A newer tag pin never replaces that historical digest. The caller retains
// the original creation record and the current policy source throughout.
func verifyHistoricalImagePolicy(ctx context.Context, c *Conn, selection *sandboxImageSelectionV1, policy *api.ImagePolicyValues) (imagePolicyReceipt, error) {
	if err := ctx.Err(); err != nil {
		return imagePolicyReceipt{}, err
	}
	if err := validateImagePolicy(policy); err != nil {
		return imagePolicyReceipt{}, err
	}
	if selection == nil {
		if policy != nil && imagePolicyString(policy.Identity) != "" {
			return imagePolicyReceipt{}, imageSelectionHistoryError()
		}
		return imagePolicyReceipt{}, nil
	}
	if err := selection.validate(); err != nil {
		return imagePolicyReceipt{}, err
	}
	if c == nil {
		return imagePolicyReceipt{}, imageSelectionHistoryError()
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return imagePolicyReceipt{}, err
	}
	if err := revalidateRetainedImagePolicy(ctx); err != nil {
		return imagePolicyReceipt{}, err
	}
	switch selection.Origin {
	case imageOriginOCI:
		receipt, err := verifyImagePolicy(ctx, selection.Image, nil, policy)
		if err != nil {
			return imagePolicyReceipt{}, err
		}
		if err := revalidateRetainedImagePolicy(ctx); err != nil {
			return imagePolicyReceipt{}, err
		}
		return receipt, nil
	case imageOriginSaved:
		if c == nil || selection.Image.SavedHost.HostID != c.HostID() {
			return imagePolicyReceipt{}, imageSelectionHistoryError()
		}
		// The caller's original saved-host branch and Runtime still require
		// the same native protected Need and root. This metadata opens neither.
		return imagePolicyReceipt{}, ctx.Err()
	case imageOriginRelease:
		// Only the original creation owner may issue this historical origin.
		// Runtime compares the exact old protected Need, not today's release.
		return imagePolicyReceipt{}, ctx.Err()
	}
	return imagePolicyReceipt{}, imageSelectionHistoryError()
}

func sameSandboxImageSelection(left, right *sandboxImageSelectionV1) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	l, le := encodeSandboxImageSelection(left)
	r, re := encodeSandboxImageSelection(right)
	return le == nil && re == nil && bytes.Equal(l, r)
}

func imageSelectionHistoryError() *UserError {
	return &UserError{Msg: "the original locally issued image selection is missing or does not match this start", Hint: "Start a new sandbox from an image whose original pin can be checked on this laptop.", Code: "image_signature", Kind: KindRefused}
}
