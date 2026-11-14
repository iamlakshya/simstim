package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLocalGitClosedRecipes(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, args := range [][]string{
		{"version"}, {"rev-parse", "--show-toplevel"}, {"rev-parse", "--absolute-git-dir"},
		{"rev-parse", "--verify", "--end-of-options", "HEAD^{commit}"},
		{"rev-parse", "--symbolic-full-name", "--verify", "--end-of-options", "--option-is-data"},
		{"rev-parse", "--path-format=absolute", "--git-path", "objects"},
		{"config", "--get", "remote.origin.url"}, {"config", "--includes", "--null", "--get-regexp", `^user\.(name|email)$`},
		{"config", "--no-includes", "--null", "--blob=" + oid, "--list"},
		{"ls-files", "--stage", "-z"}, {"ls-files", "-z", "--cached", "--others", "--exclude-standard"},
		{"read-tree", "--empty"}, {"write-tree"}, {"update-index", "--add", "-z", "--index-info"},
		{"commit-tree", "--no-gpg-sign", oid, "-p", oid}, {"hash-object", "--no-filters", "-w", "--stdin"},
		{"hash-object", "-w", "-t", "tree", "--stdin"}, {"check-ref-format", "refs/heads/topic"},
		{"cat-file", "blob", oid}, {"cat-file", "-e", oid + "^{commit}"}, {"cat-file", "-t", oid}, {"cat-file", "-s", oid},
		{"merge-base", "--is-ancestor", oid, oid}, {"rev-list", "--count", "topic", "^HEAD"},
		{"symbolic-ref", "--quiet", "--short", "HEAD"}, {"worktree", "list", "--porcelain", "-z"},
		{"update-ref", "refs/heads/topic", oid}, {"update-ref", "--no-deref", "-m", "pull reviewed session work", "refs/heads/topic", oid, oid},
		{"bundle", "verify", filepath.Join(t.TempDir(), "input.bundle")},
		{"bundle", "list-heads", filepath.Join(t.TempDir(), "input.bundle")}, {"bundle", "create", "-", "refs/heads/topic"},
		{"init", "--bare", "--template=", "--object-format=sha256", t.TempDir()},
		{"diff-tree", "-r", "--no-renames", "--no-commit-id", "--no-abbrev", "--raw", "-z", oid, oid},
		{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=0", "--no-color", oid, oid, "--", "--literal-file"},
		{"-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--atomic", "--", t.TempDir(), "refs/heads/topic/*:refs/private/*"},
	} {
		if !localGitRecipe(args) {
			t.Errorf("actual closed recipe refused: %q", args)
		}
	}
	for _, args := range [][]string{
		nil, {"version", "--help"}, {"shell"}, {"clone", "ext::helper"}, {"fetch", "https://example.invalid"},
		{"-c", "core.sshCommand=command", "version"}, {"config", "--get", "credential.helper"},
		{"commit-tree", "--gpg-sign", oid, "-p", oid}, {"hash-object", "-w", "--path=filter-me", "--stdin"},
		{"diff", "--ext-diff", oid, oid}, {"check-ref-format", "--branch"}, {"rev-list", "--count", "--help", "^HEAD"},
		{"bundle", "verify", "relative.bundle"}, {"update-ref", "--stdin"},
	} {
		if localGitRecipe(args) {
			t.Errorf("unclosed operation accepted: %q", args)
		}
	}
}

func TestLocalGitPrivateLaunchFraming(t *testing.T) {
	request := gitMetadataTestRequest(t, "/ordinary/git", "/ordinary/repo", "version")
	for i, entry := range request.Env {
		if strings.HasPrefix(entry, "HOME=") {
			request.Env[i] = "HOME=literal\nvalue"
		}
	}
	var data bytes.Buffer
	if err := writeLocalGitRequest(&data, request); err != nil {
		t.Fatal(err)
	}
	data.WriteByte('G')
	read, err := readLocalGitRequest(&data)
	if err != nil || !slices.Equal(read.Env, request.Env) {
		t.Fatalf("private request changed: %+v, %v", read, err)
	}
	if next, err := data.ReadByte(); err != nil || next != 'G' {
		t.Fatal("decoder consumed the separate launch gate")
	}
	for _, size := range []uint32{0, localGitControlMax + 1} {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], size)
		if _, err := readLocalGitRequest(bytes.NewReader(prefix[:])); err == nil {
			t.Fatal("invalid control size was accepted")
		}
	}
	if _, err := readLocalGitRequest(io.LimitReader(bytes.NewReader([]byte{0, 0, 0, 4, '{'}), 5)); err == nil {
		t.Fatal("partial launch metadata was accepted")
	}
}

func TestLocalGitOutputCannotChangeAfterIncompleteReturn(t *testing.T) {
	var output bytes.Buffer
	sink := &localGitSink{target: &output}
	if _, err := sink.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	sink.close()
	if _, err := sink.Write([]byte("after")); !errors.Is(err, io.ErrClosedPipe) || output.String() != "before" {
		t.Fatal("unfinished copier could still change returned metadata")
	}
}

type localGitTestSource struct {
	path string
	err  error
}

func (s localGitTestSource) Path() string                     { return s.path }
func (s localGitTestSource) Revalidate(context.Context) error { return s.err }
func (s localGitTestSource) Close() error                     { return nil }
