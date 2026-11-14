package client

import (
	"context"
	"errors"
	"sync"
)

// LocalGitSource is an opaque read-only source capability, held until actual
// owned Git completion. It supplies no process or permission to fetch values.
type LocalGitSource interface {
	Path() string
	Revalidate(context.Context) error
	Close() error
}

// LocalGitSourceOpener is supplied by the application composition root.
// Linux uses the canonical host inventory/guard, never another manifest parser.
type LocalGitSourceOpener func(context.Context, string, bool) (LocalGitSource, error)

var localSourceProvider struct {
	sync.Mutex
	open     LocalGitSourceOpener
	metadata LocalGitMetadataOpener
	used     bool
}

// InstallLocalGitSources installs the ONE immutable process dependency before
// any operation. It cannot be replaced by an environment/context flag or row.
// Applications and corresponding test entrypoints wire the actual provider.
func InstallLocalGitSources(open LocalGitSourceOpener, metadata ...LocalGitMetadataOpener) error {
	localSourceProvider.Lock()
	defer localSourceProvider.Unlock()
	if open == nil || len(metadata) > 1 || len(metadata) == 1 && metadata[0] == nil || localSourceProvider.open != nil || localSourceProvider.used {
		return &UserError{Code: "source_provider", Msg: "the local source capability provider is missing or already fixed",
			Hint: "Start the command through its canonical application entrypoint.", Kind: KindRefused}
	}
	localSourceProvider.open = open
	if len(metadata) == 1 {
		localSourceProvider.metadata = metadata[0]
	}
	return nil
}

func openGitMetadataSource(ctx context.Context, supplied []LocalGitMetadataOpener) (LocalGitMetadataSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(supplied) > 1 {
		return nil, gitMetadataFailure()
	}
	localSourceProvider.Lock()
	localSourceProvider.used = true
	open := localSourceProvider.metadata
	localSourceProvider.Unlock()
	if len(supplied) == 1 {
		open = supplied[0] // actual original-snapshot lease factory, never a name map
	}
	if open == nil {
		return nil, gitMetadataFailure()
	}
	source, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, gitMetadataFailure()
	}
	return source, nil
}

func openLocalSource(ctx context.Context, path string, graph bool) (LocalGitSource, error) {
	localSourceProvider.Lock()
	localSourceProvider.used = true
	open := localSourceProvider.open
	localSourceProvider.Unlock()
	if open == nil {
		return nil, &UserError{Code: "source_inventory", Msg: "the local writable-source inventory is unknown",
			Hint: "Use the canonical application entrypoint with its read-only source provider.", Kind: KindRefused}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return open(ctx, path, graph)
}

// RunLocalGitMetadata uses the same source capability, safe Git definition and
// native owner as review and laptop capture. Front ends must not bypass it with
// a raw PATH Git command even for a repository/branch preflight.
func RunLocalGitMetadata(ctx context.Context, args ...string) (_ []byte, err error) {
	git, err := openLocalGit(ctx, "")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, git.close()) }()
	return git.run(ctx, nil, pullMetadataMax, args...)
}
