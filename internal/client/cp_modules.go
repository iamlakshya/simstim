package client

import (
	"context"
	"errors"
	"os"
)

func (o *CopyOut) parseModules(ctx context.Context, input []byte) (_ [][]byte, returned error) {
	directory, err := os.MkdirTemp("", "copy-config-")
	if err != nil {
		return nil, err
	}
	metadata := func(ctx context.Context) (LocalGitMetadataSource, error) {
		return newOrdinaryLocalGitMetadataSource(ctx, o.plan.operation)
	}
	git, err := openLocalGit(ctx, directory, metadata)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	defer func() {
		returned = errors.Join(returned, git.close())
		if err := git.ownership.quiescent(); err != nil {
			returned = errors.Join(returned, err)
		} else {
			returned = errors.Join(returned, os.RemoveAll(directory))
		}
	}()
	data, err := git.run(ctx, input, pullMetadataMax, "config", "--no-includes", "--null", "--file=-", "--list")
	if err != nil {
		return nil, err
	}
	return gitConfigValues(data), nil
}
