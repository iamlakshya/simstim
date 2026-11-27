package client

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

type pullHead struct{ remote, quarantine, oid string }

func pullNonce() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("make pull identity: %w", err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes[:])), nil
}

func pullNamespace(primary string) string { return "refs/heads/" + brand.Name + "/" + primary + "/" }

func (g *pullGit) bundleHeads(ctx context.Context, path, prefix string) ([]pullHead, error) {
	data, err := g.run(ctx, nil, pullMetadataMax, "bundle", "list-heads", path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var heads []pullHead
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 || !api.ValidGitOID(parts[0]) {
			return nil, pullProtocol("the bundle has an invalid ref header")
		}
		name := parts[1]
		if !utf8.ValidString(name) {
			return nil, pullProtocol("the bundle ref name is not unambiguous UTF-8")
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if name == prefix || seen[name] {
			return nil, pullProtocol("the bundle has an ambiguous session ref")
		}
		if _, err = g.run(ctx, nil, 4096, "check-ref-format", name); err != nil {
			return nil, pullProtocol("the bundle has an invalid session ref")
		}
		seen[name] = true
		heads = append(heads, pullHead{remote: name, oid: parts[0]})
	}
	if len(heads) == 0 {
		return nil, pullProtocol("the bundle has no session namespace refs")
	}
	return heads, nil
}

func (g *pullGit) quarantine(ctx context.Context, download *downloadedPull, host string) ([]pullHead, string, error) {
	prefix := pullNamespace(download.export.Primary)
	heads, err := g.bundleHeads(ctx, download.path, prefix)
	if err != nil {
		return nil, "", err
	}
	nonce, err := pullNonce()
	if err != nil {
		return nil, "", err
	}
	destination := "refs/" + brand.Name + "/sessions/" + HostFileKey(host) + "/" + download.export.Primary + "/" + nonce + "/"
	args := []string{"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--atomic", "--", download.path, prefix + "*:" + destination + "*"}
	// The canonical hook policy is applied by run. fsck checks stay explicit
	// for both the incoming transfer and the fetch's object validation.
	args = append([]string{"-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true"}, args...)
	if _, err = g.run(ctx, nil, pullMetadataMax, args...); err != nil {
		return nil, "", err
	}
	selected := ""
	for i := range heads {
		heads[i].quarantine = destination + strings.TrimPrefix(heads[i].remote, prefix)
		actual, err := g.ref(ctx, heads[i].quarantine)
		if err != nil || actual != heads[i].oid {
			return nil, "", pullProtocol("fetched session ref does not match the verified bundle")
		}
		kind, err := g.run(ctx, nil, 4096, "cat-file", "-t", heads[i].oid)
		if err != nil || gitLine(kind) != "commit" {
			return nil, "", pullProtocol("a session branch is not a commit")
		}
		if heads[i].remote == download.export.DefaultRef {
			selected = heads[i].quarantine
		}
	}
	if selected == "" {
		return nil, "", pullProtocol("the claimed current session branch is not in the bundle")
	}
	return heads, selected, nil
}

func (g *pullGit) reviewBase(ctx context.Context, seeds []string, tip string) (string, error) {
	for _, seed := range seeds {
		if !g.hasCommit(ctx, seed) {
			continue
		}
		yes, err := g.ancestor(ctx, seed, tip)
		if err != nil {
			return "", err
		}
		if yes {
			return seed, nil
		}
	}
	tree, err := g.run(ctx, nil, 4096, "hash-object", "-w", "-t", "tree", "--stdin")
	return gitLine(tree), err
}

func (g *pullGit) branchState(ctx context.Context, into string) (string, string, error) {
	target := "refs/heads/" + into
	if _, err := g.run(ctx, nil, 4096, "check-ref-format", target); err != nil {
		return "", "", &UserError{Msg: "the local branch name is invalid", Hint: "Choose a plain branch name with --into.", Code: "bad_branch", Kind: KindUsage}
	}
	old, err := g.ref(ctx, target)
	if err != nil && !(gitExit(err, 1) || gitExit(err, 128)) {
		return "", "", err
	}
	if err != nil {
		old = ""
	}
	return target, old, nil
}

func (g *pullGit) checkedOut(ctx context.Context, target string) error {
	data, err := g.run(ctx, nil, pullMetadataMax, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, field := range strings.Split(string(data), "\x00") {
		if field == "branch "+target {
			return &UserError{Msg: "the target branch is checked out in a local worktree", Hint: "Choose another branch with --into, or switch that worktree first.", Code: "branch_checked_out", Kind: KindRefused}
		}
	}
	return nil
}

func (g *pullGit) moveBranch(ctx context.Context, target, old, tip string, force bool) error {
	if err := g.checkedOut(ctx, target); err != nil {
		return err
	}
	if old != "" && !force {
		yes, err := g.ancestor(ctx, old, tip)
		if err != nil {
			return err
		}
		if !yes {
			return &UserError{Msg: "the local target has commits that the session branch does not have", Hint: "Choose another --into branch, or replace it with --force.", Code: "branch_diverged", Kind: KindRefused}
		}
	}
	expected := old
	if expected == "" {
		expected = strings.Repeat("0", len(tip))
	}
	if _, err := g.run(ctx, nil, 64<<10, "update-ref", "--no-deref", "-m", "pull reviewed session work", target, tip, expected); err != nil {
		return &UserError{Msg: "the local branch changed before the reviewed update completed", Hint: "Review the current branch and pull again.", Code: "pull_changed", Kind: KindRefused}
	}
	return nil
}
