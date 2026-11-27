package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

// Hash the FULL canonical summary, including Digest. This is distinct from
// the manifest digest, whose own Digest field is necessarily excluded. It
// retains no summary/file list and supplies no selection or Finish authority.
func sandboxInputSummaryHash(ctx context.Context, summary api.InputSummary, copyMax, seedMax int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := summary.Validate(copyMax, seedMax); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}
