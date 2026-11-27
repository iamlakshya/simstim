//go:build linux

package publicationlease

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// ResumeRemovalV1 resumes only an original canonical deletion intent that
// the operation owner persisted AFTER a fresh EX fence proof, or an original
// creating intent that could never have lent a helper. The caller verifies
// that protected phase under its original control transaction. This function
// cannot create such an intent or establish closure from a missing live leaf.
// A present leaf always needs its exact identity, complete record and fresh EX.
// An absent leaf can only finish this intent's directory sync; it grants no
// removal of another path and no claim that helpers have ended.
func ResumeRemovalV1(ctx context.Context, root *os.Root, domain cachefs.Domain, encoded string) (*ExclusiveV1, error) {
	if ctx == nil || root == nil {
		return nil, failureV1("the retirement has no original control root")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, err := DecodeRecordV1(encoded)
	if err != nil {
		return nil, err
	}
	if record.Domain != domain {
		return nil, failureV1("the retirement belongs to another host domain")
	}
	if err := record.Root.CheckRoot(root); err != nil {
		return nil, err
	}
	_, err = root.Lstat(nameV1(record.Token))
	if !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return nil, err
		}
		return TryExclusiveV1(ctx, root, domain, encoded)
	}
	owned, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if err := record.Root.CheckRoot(owned); err != nil {
		owned.Close()
		return nil, err
	}
	return &ExclusiveV1{root: owned, record: record, encoded: encoded, removed: true}, nil
}
