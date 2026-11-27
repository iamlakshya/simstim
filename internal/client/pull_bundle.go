package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/review"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
)

// PullOptions contains explicit local review and branch-update choices.
// SecretValues must come from the canonical authorised producer, never the
// guest. The pull protocol never sends them to a host or writes them down.
type PullOptions struct {
	metadata LocalGitMetadataOpener // actual original local review/settings lease factory
	// Repo is the protected guest basename. Repository below is LOCAL.
	Repo         string
	Repository   string
	Into         string
	Force        bool
	AllowFlagged bool
	Limit        int64
	ProjectFiles []string
	SecretValues [][]byte
	// ReadSecrets runs once after verified quarantine and before scanning.
	// It transfers ephemeral local byte copies from the closed review gate.
	// Empty exports never invoke it. No value travels through the peer.
	ReadSecrets func(context.Context) ([][]byte, error)
}

// SandboxPullResult reports immutable quarantine and reviewed branch state.
// Findings hold names and reasons only, never source excerpts or secrets.
type SandboxPullResult struct {
	Empty          bool             `json:"empty"`
	Full           bool             `json:"full"`
	Branch         string           `json:"branch,omitempty"`
	Tip            string           `json:"tip,omitempty"`
	Base           string           `json:"base,omitempty"`
	Quarantine     []string         `json:"quarantine,omitempty"`
	Findings       []review.Finding `json:"findings,omitempty"`
	Changed        int              `json:"changed"`
	Moved          bool             `json:"moved"`
	Acknowledged   bool             `json:"acknowledged"`
	ReviewComplete bool             `json:"review_complete"`
}

// PullReviewError retains a safe review summary when movement was refused.
type PullReviewError struct {
	Result  *SandboxPullResult
	Failure *UserError
}

func (e *PullReviewError) Error() string { return e.Failure.Error() }

// Unwrap exposes the canonical error kind and retry hint.
func (e *PullReviewError) Unwrap() error { return e.Failure }

// PullSandbox fetches a sandbox's bundle into quarantine, reviews it as data
// and moves exactly one local branch only after every required consent.
func (c *Conn) PullSandbox(ctx context.Context, id string, options PullOptions) (*SandboxPullResult, error) {
	if readLiteral := options.ReadSecrets; readLiteral != nil {
		options.ReadSecrets = func(attempt context.Context) ([][]byte, error) {
			return combinedPullReviewValues(attempt, func(local context.Context) ([]api.Secret, error) {
				return c.ReviewPublicStandIns(local, id)
			}, readLiteral)
		}
	}
	return pullSandbox(ctx, c, c.Host.Name, id, options)
}

func pullSandbox(ctx context.Context, peer pullPeer, host, id string, options PullOptions) (_ *SandboxPullResult, err error) {
	caller := ctx
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = operation
	var keepalive *pullKeepalive
	limit := options.Limit
	if limit <= 0 {
		limit = api.PullMaxDefault
	}
	var metadata []LocalGitMetadataOpener
	if options.metadata != nil {
		metadata = append(metadata, options.metadata)
	}
	repository, err := openPullGit(ctx, options.Repository, metadata...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, repository.close()) }()
	download, err := downloadPull(ctx, peer, id, false, "", limit, options.Repo)
	if err != nil {
		return nil, err
	}
	download.ownership = repository.ownership
	acknowledged := false
	defer func() {
		failure := keepalive.stop()
		if failure != nil && errors.Is(err, context.Canceled) && caller.Err() == nil {
			err = failure
		}
		err = errors.Join(err, download.close())
		if !acknowledged {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = finishPull(cleanup, peer, download, nil, true)
		}
	}()
	result := &SandboxPullResult{Empty: download.export.Empty, Full: download.export.Full}
	if result.Empty {
		if err = finishPull(ctx, peer, download, nil, false); err != nil {
			return result, err
		}
		acknowledged = true
		result.Acknowledged = true
		return result, nil
	}
	keepalive = startPullKeepalive(ctx, peer, download.export, cancel)
	header, err := pullHeader(download.path)
	if err != nil {
		return result, pullProtocol("the session bundle has an invalid native header")
	}
	if download.export.Full && len(header.Prerequisites) != 0 {
		return result, pullProtocol("the full bundle retained a prerequisite")
	}
	if !trustedPrerequisites(download.export.SeedHeads, header.Prerequisites) {
		return result, pullProtocol("the incremental bundle named history outside its protected seed roots")
	}
	firstHeads := namespaceHeads(header, pullNamespace(download.export.Primary))
	if err = repository.verify(ctx, download.path); err != nil {
		if !missingSeedPrerequisites(ctx, repository, download.export.SeedHeads, header.Prerequisites) {
			return result, &UserError{Msg: "the session bundle did not pass native Git verification", Hint: "Pull again after checking the session's repository.", Code: "pull_verify", Kind: KindRefused}
		}
		if failure := keepalive.stop(); failure != nil {
			return result, failure
		}
		keepalive = nil
		replacement, retryErr := downloadPull(ctx, peer, id, true, download.export.Token, limit, options.Repo)
		if retryErr != nil {
			return result, retryErr
		}
		if err := download.close(); err != nil {
			// The replacement has not been used by Git. Retain the original
			// capture and its actual owner when cleanup is still uncertain.
			return result, errors.Join(err, replacement.close())
		}
		download = replacement
		download.ownership = repository.ownership
		result.Full = true
		result.Empty = download.export.Empty
		keepalive = startPullKeepalive(ctx, peer, download.export, cancel)
		if result.Empty {
			return result, pullProtocol("a full-history retry unexpectedly removed all exported work")
		}
		header, err = pullHeader(download.path)
		if err != nil || len(header.Prerequisites) != 0 || !reflect.DeepEqual(firstHeads, namespaceHeads(header, pullNamespace(download.export.Primary))) {
			return result, pullProtocol("the full bundle changed its captured heads or retained prerequisites")
		}
		if err = repository.verify(ctx, download.path); err != nil {
			return result, &UserError{Msg: "the full session bundle did not pass native Git verification", Hint: "Check the session repository before pulling again.", Code: "pull_verify", Kind: KindRefused}
		}
	}
	heads, selected, err := repository.quarantine(ctx, download, host)
	if err != nil {
		return result, err
	}
	for _, head := range heads {
		result.Quarantine = append(result.Quarantine, head.quarantine)
	}
	tip, err := repository.ref(ctx, selected)
	if err != nil {
		return result, err
	}
	result.Tip = tip
	into := options.Into
	if into == "" {
		into = strings.TrimPrefix(download.export.DefaultRef, "refs/heads/")
	}
	target, old, err := repository.branchState(ctx, into)
	if err != nil {
		return result, err
	}
	result.Branch = into
	result.Base, err = repository.reviewBase(ctx, download.export.SeedHeads, tip)
	if err != nil {
		return result, err
	}
	var localValues [][]byte
	if options.ReadSecrets != nil {
		localValues, err = options.ReadSecrets(ctx)
		defer func() {
			for _, value := range localValues {
				clear(value)
			}
		}()
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			var failure *UserError
			if !errors.As(err, &failure) {
				failure = &UserError{Msg: "the local secret review is incomplete", Hint: "Inspect the retained quarantine and restore the original local review authority before pulling again.", Code: "pull_review_incomplete", Kind: KindRefused}
			}
			return result, &PullReviewError{Result: result, Failure: failure}
		}
	}
	rules, err := review.DefaultRules()
	if err != nil {
		return result, fmt.Errorf("load static pull review: %w", err)
	}
	secrets := append(append([][]byte(nil), options.SecretValues...), localValues...)
	scanner, err := review.New(rules, review.Options{ProjectFiles: options.ProjectFiles, Secrets: secrets, Brand: brand.Name})
	if err != nil {
		return result, err
	}
	defer scanner.Clear()
	result.Findings, result.Changed, err = repository.review(ctx, result.Base, tip, scanner)
	if err != nil {
		return result, err
	}
	result.ReviewComplete = true
	if failure := reviewConsent(result, options.AllowFlagged); failure != nil {
		return result, failure
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = repository.moveBranch(ctx, target, old, tip, options.Force); err != nil {
		return result, err
	}
	result.Moved = true
	commits := []string{tip}
	if failure := keepalive.stop(); failure != nil {
		return result, failure
	}
	keepalive = nil
	if err = finishPull(ctx, peer, download, commits, false); err != nil {
		return result, &UserError{Msg: "the reviewed branch moved locally, but the host did not record the fetched commits", Hint: "Keep the local quarantine refs and pull again before removing the sandbox.", Code: "pull_ack", Kind: KindFailed}
	}
	acknowledged = true
	result.Acknowledged = true
	return result, nil
}

func trustedPrerequisites(seeds, prerequisites []string) bool {
	trusted := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		trusted[seed] = true
	}
	for _, oid := range prerequisites {
		if !trusted[oid] {
			return false
		}
	}
	return true
}

func reviewConsent(result *SandboxPullResult, allow bool) error {
	for _, finding := range result.Findings {
		if finding.Refused {
			return &PullReviewError{Result: result, Failure: &UserError{Msg: "the pull contains a tree path or Git module value that must be refused", Hint: "Fix the flagged work in the sandbox, then pull again.", Code: "pull_modules", Kind: KindRefused}}
		}
	}
	if len(result.Findings) > 0 && !allow {
		return &PullReviewError{Result: result, Failure: &UserError{Msg: "the work changes files or content that need explicit review", Hint: "Review the quarantine diff, then repeat pull with --allow-flagged.", Code: "pull_flagged", Kind: KindRefused}}
	}
	return nil
}

func missingSeedPrerequisites(ctx context.Context, repo *pullGit, seeds, prerequisites []string) bool {
	trusted := map[string]bool{}
	for _, seed := range seeds {
		trusted[seed] = true
	}
	for _, required := range prerequisites {
		if trusted[required] && !repo.hasCommit(ctx, required) {
			return true
		}
	}
	return false
}

func pullHeader(path string) (gitpolicy.BundleHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return gitpolicy.BundleHeader{}, err
	}
	defer file.Close()
	return gitpolicy.ReadBundleHeader(file, pullMetadataMax)
}

func namespaceHeads(header gitpolicy.BundleHeader, prefix string) map[string]string {
	result := map[string]string{}
	for name, oid := range header.Heads {
		if strings.HasPrefix(name, prefix) {
			result[name] = oid
		}
	}
	return result
}
