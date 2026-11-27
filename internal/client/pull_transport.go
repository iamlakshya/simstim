package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type pullPeer interface {
	Call(context.Context, string, any, any) error
	HasFeature(string) bool
}

type downloadedPull struct {
	export    api.PullExport
	directory string
	path      string
	ownership *localGitOwnership
}

func (p *downloadedPull) close() error {
	if p != nil && p.directory != "" {
		if err := p.ownership.quiescent(); err != nil {
			return err
		}
		if err := os.RemoveAll(p.directory); err != nil {
			return fmt.Errorf("remove the owned pull capture: %w", err)
		}
		p.directory = ""
	}
	return nil
}

func downloadPull(ctx context.Context, peer pullPeer, id string, full bool, previous string, limit int64, repository string) (_ *downloadedPull, err error) {
	if repository != "" && !api.InputRepoName(repository) {
		return nil, &UserError{Code: "pull_repo", Msg: "--repo needs a protected repository basename", Hint: "Use a basename from the sandbox's repository list.", Kind: KindUsage}
	}
	if repository != "" && !peer.HasFeature(api.FeaturePullRepositories) {
		return nil, &UserError{Code: "helper_old", Msg: "the helper cannot select a protected pull repository", Hint: "Update the host helper before using --repo.", Kind: KindRefused}
	}
	if !peer.HasFeature(api.FeaturePullBundle) {
		return nil, &UserError{Msg: "the host helper has no bundle pull support", Hint: "Update the host helper, then pull again.", Code: "helper_old", Kind: KindFailed}
	}
	token, err := pullNonce()
	if err != nil {
		return nil, err
	}
	descriptor := api.PullExport{ID: id, Token: token}
	owned := &downloadedPull{export: descriptor}
	defer func() {
		if err != nil {
			owned.close()
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			var ignored api.PulledResult
			_ = peer.Call(cleanup, api.OpPulled, api.PulledArgs{ID: id, Token: token, Abort: true}, &ignored)
		}
	}()
	if err = peer.Call(ctx, api.OpPullBegin, api.PullBeginArgs{ID: id, Token: token, Full: full, Previous: previous, Repo: repository}, &descriptor); err != nil {
		return nil, err
	}
	owned.export = descriptor
	if descriptor.Token != token || descriptor.Repo != repository {
		return nil, pullProtocol("the host changed the pull operation identity")
	}

	if err = descriptor.Validate(id, limit); err != nil {
		return nil, pullProtocol("the host returned an invalid bundle export")
	}
	if full && !descriptor.Full {
		return nil, pullProtocol("the host changed the requested bundle history mode")
	}
	if descriptor.Empty {
		return owned, nil
	}
	directory, err := os.MkdirTemp("", "bundle-pull-")
	if err != nil {
		return nil, fmt.Errorf("make private pull folder: %w", err)
	}
	owned.directory = directory
	owned.path = filepath.Join(directory, "work.bundle")
	file, err := os.OpenFile(owned.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("make private bundle: %w", err)
	}
	defer file.Close()
	var offset int64
	for offset < descriptor.Size {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var chunk api.PullChunk
		if err = peer.Call(ctx, api.OpPullRead, api.PullReadArgs{ID: id, Token: descriptor.Token, Offset: offset}, &chunk); err != nil {
			return nil, err
		}
		if len(chunk.Data) == 0 || len(chunk.Data) > api.PullReadMax || chunk.Next != offset+int64(len(chunk.Data)) || chunk.Next > descriptor.Size || chunk.EOF != (chunk.Next == descriptor.Size) {
			return nil, pullProtocol("the host returned a malformed or unfinished bundle chunk")
		}
		if _, err = file.Write(chunk.Data); err != nil {
			return nil, fmt.Errorf("write private bundle: %w", err)
		}
		offset = chunk.Next
	}
	if err = file.Sync(); err != nil {
		return nil, fmt.Errorf("sync private bundle: %w", err)
	}
	if err = file.Close(); err != nil {
		return nil, fmt.Errorf("close private bundle: %w", err)
	}
	return owned, nil
}

func finishPull(ctx context.Context, peer pullPeer, p *downloadedPull, commits []string, abort bool) error {
	if p == nil || p.export.Token == "" {
		return nil
	}
	var result api.PulledResult
	return peer.Call(ctx, api.OpPulled, api.PulledArgs{ID: p.export.ID, Token: p.export.Token, Commits: commits, Abort: abort}, &result)
}
