package inputtransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// These tests exercise real staged/rooted file IO with a synthetic bundle.
// They are explicitly not native Git, UID, backend or Store ordering proofs.
func stagedIncludeFixture(t *testing.T, relative string) (ReceiveOptions, api.InputSummary, string, []byte) {
	t.Helper()
	data, bundle := []byte("TOKEN=fixture\n"), []byte("synthetic bundle")
	hash, bundleHash := sha256.Sum256(data), sha256.Sum256(bundle)
	plan := api.SessionInputs{Version: api.SessionInputsV1, Nonce: api.InputNonce(strings.Repeat("a", 64)), Identity: api.GitIdentity{Known: true},
		Files: []api.InputFile{{Path: "/work/source/" + relative, Size: int64(len(data)), Mode: 0600, SHA256: hex.EncodeToString(hash[:]), Include: true}},
		Repositories: []api.InputRepository{{Name: "source", SourceBranch: "refs/heads/main", Commit: strings.Repeat("b", 40), ObjectFormat: "sha1",
			Size: int64(len(bundle)), SHA256: hex.EncodeToString(bundleHash[:])}}}
	summary, err := plan.Summary(api.DefaultCopyMax, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Session: "session2", Nonce: plan.Nonce, Digest: summary.Digest}
	var input bytes.Buffer
	input.Write(declarationWire(t, plan, scope))
	writer := proto.NewWriter(&input)
	if err = sendItem(t.Context(), writer, item{Kind: "include", Size: int64(len(data)), SHA256: plan.Files[0].SHA256}, func(out io.Writer) error { _, err := out.Write(data); return err }); err != nil {
		t.Fatal(err)
	}
	if err = sendItem(t.Context(), writer, item{Kind: "repository", Size: int64(len(bundle)), SHA256: plan.Repositories[0].SHA256}, func(out io.Writer) error { _, err := out.Write(bundle); return err }); err != nil {
		t.Fatal(err)
	}
	if err = sendEvent(t.Context(), writer, scope, "input.data_end", 2, nil); err != nil {
		t.Fatal(err)
	}
	options, directory := receiverFixture(t)
	options.Scope = scope
	var output bytes.Buffer
	if _, err = Receive(t.Context(), &input, &output, options); err != nil {
		t.Fatal(err)
	}
	return options, summary, directory, data
}

func TestReviewedIncludeStaysLooseUntilCloneThenApplies(t *testing.T) {
	options, summary, directory, data := stagedIncludeFixture(t, ".env")
	target := filepath.Join(directory, "work", "source", ".env")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("include was written before its native clone existed")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ApplyIncludes(t.Context(), options, summary); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("loose include = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "work", "source", ".git")); !os.IsNotExist(err) {
		t.Fatal("the file-copy helper unexpectedly ran Git or created a commit")
	}
}

func TestReviewedIncludeTamperAndGitAliasCannotReplaceExistingData(t *testing.T) {
	options, summary, directory, data := stagedIncludeFixture(t, ".env")
	target := filepath.Join(directory, "work", "source", ".env")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := options.Stage.WriteFile(IncludeName(0), bytes.Repeat([]byte("x"), len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyIncludes(t.Context(), options, summary); err == nil {
		t.Fatal("changed staged include accepted")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old" {
		t.Fatal("failed include replaced old working-tree data")
	}
	options, summary, directory, _ = stagedIncludeFixture(t, "alias/config")
	repository := filepath.Join(directory, "work", "source")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".git", "config"), []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(repository, "alias")); err != nil {
		t.Skip(err)
	}
	if err := ApplyIncludes(t.Context(), options, summary); err == nil {
		t.Fatal("destination alias entered clone Git metadata")
	}
	if got, err := os.ReadFile(filepath.Join(repository, ".git", "config")); err != nil || string(got) != "untouched" {
		t.Fatal("Git config was overwritten through an include alias")
	}
}
