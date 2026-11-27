package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// hostRuleOriginData is local selection metadata. The original owner retains
// its settings operation and revalidates its real input source when it binds
// a selected host. These strings are never source or grant authority.
type hostRuleOriginData struct {
	origin      string
	originalCwd string
}

// readHostRuleOrigin borrows the original physical settings source. It uses
// the actual protected seed/metadata readers. It never uses a host directory,
// reads a secondary seed or runs Git with the repository as its working dir.
func readHostRuleOrigin(ctx context.Context, operation *settingsOperation, options NewInputOptions) (_ hostRuleOriginData, err error) {
	if ctx == nil || operation == nil {
		return hostRuleOriginData{}, grantOperationError()
	}
	metadata, err := newOrdinaryLocalGitMetadataSource(ctx, operation)
	if err != nil {
		return hostRuleOriginData{}, err
	}
	defer func() { err = errors.Join(err, metadata.Close()) }()
	if err := metadata.CheckKeys(ctx, gitMetadataCheckKeys(localWindows())); err != nil {
		return hostRuleOriginData{}, err
	}
	if err := metadata.Revalidate(ctx); err != nil {
		return hostRuleOriginData{}, err
	}
	cwd := options.OriginalCWD
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return hostRuleOriginData{}, hostRuleOriginFailure(err)
		}
	}
	if !filepath.IsAbs(cwd) {
		return hostRuleOriginData{}, copyInputError("input_cwd", "the host rule has no original absolute laptop directory")
	}
	opener := func(ctx context.Context) (LocalGitMetadataSource, error) {
		return newOrdinaryLocalGitMetadataSource(ctx, operation)
	}
	result := hostRuleOriginData{originalCwd: cwd}
	if len(options.Seeds) != 0 {
		// HostClone affects the session's primary policy, not host selection.
		// The first explicit laptop seed still selects the host, even with
		// --dir. The existing source producer performs all native checks.
		discovery, err := discoverSessionSeeds(ctx, cwd, options.Seeds[:1], false, opener)
		if err != nil {
			return hostRuleOriginData{}, hostRuleOriginFailure(err)
		}
		if first := discovery.primary(); first != nil {
			result.origin = first.origin
		}
	} else {
		marker, err := repositoryMarker(cwd)
		if err != nil {
			return hostRuleOriginData{}, hostRuleOriginFailure(err)
		}
		if marker != "" {
			fact, err := repositorySourceProvenance(ctx, marker, opener)
			if err != nil {
				return hostRuleOriginData{}, hostRuleOriginFailure(err)
			}
			result.origin = fact.origin
		}
	}
	if err := operation.Revalidate(ctx); err != nil {
		return hostRuleOriginData{}, err
	}
	return result, nil
}

func hostRuleOriginFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	return errors.Join(&UserError{Msg: "the laptop repository for the host rule could not be read",
		Hint: "Check the first --seed folder, or run new from the intended local repository.", Code: "repo_origin", Kind: KindFailed}, err)
}
