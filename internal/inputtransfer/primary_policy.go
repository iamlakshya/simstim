package inputtransfer

import (
	"context"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/repopolicy"
	"path"
	"reflect"
)

// VerifyPrimaryPolicy runs only inside the existing fixed-account apply helper,
// after native clones/includes and before the exact completion/start handoff.
// The canonical reader refuses every symlink/special file; no root access or
// second policy parser is introduced here.
func VerifyPrimaryPolicy(ctx context.Context, control Control, summary api.InputSummary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if control.PrimaryRepository == "" {
		return nil
	}
	if len(summary.Repositories) == 0 || summary.Repositories[0].Name != control.PrimaryRepository || !api.InputRepoName(control.PrimaryRepository) {
		return protocol("the original primary policy scope changed")
	}
	expected := summary.Repositories[0].Policy
	repo := control.PrimaryRepository
	if expected != nil {
		repo = expected.Repo
	}
	actual, err := repopolicy.Read(path.Join("/work", control.PrimaryRepository), repo)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, actual) {
		return protocol("the cloned primary policy differs from its exact selected declaration")
	}
	return ctx.Err()
}
